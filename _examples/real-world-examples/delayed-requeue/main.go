package main

import (
	"context"
	stdSQL "database/sql"
	"fmt"
	"math/rand"
	"time"

	"github.com/ThreeDotsLabs/watermill/message/router/middleware"

	"github.com/brianvoe/gofakeit/v6"
	_ "github.com/lib/pq"
	"github.com/redis/go-redis/v9"

	"github.com/ThreeDotsLabs/watermill"
	"github.com/ThreeDotsLabs/watermill-redisstream/pkg/redisstream"
	"github.com/ThreeDotsLabs/watermill-sql/v4/pkg/sql"
	"github.com/ThreeDotsLabs/watermill/components/cqrs"
	"github.com/ThreeDotsLabs/watermill/message"
)

func main() {
	db, err := stdSQL.Open("postgres", "postgres://watermill:password@postgres:5432/watermill?sslmode=disable")
	if err != nil {
		panic(err)
	}

	logger := watermill.NewStdLogger(false, false)

	redisClient := redis.NewClient(&redis.Options{Addr: "redis:6379"})

	redisPublisher, err := redisstream.NewPublisher(redisstream.PublisherConfig{
		Client: redisClient,
	}, logger)
	if err != nil {
		panic(err)
	}

	delayedRequeuer, err := sql.NewPostgreSQLDelayedRequeuer(sql.DelayedRequeuerConfig{
		DB:        db,
		Publisher: redisPublisher,
		DelayOnError: &middleware.DelayOnError{
			InitialInterval: 10 * time.Second,
			MaxInterval:     3 * time.Minute,
			Multiplier:      2,
		},
		Logger: logger,
	})
	if err != nil {
		panic(err)
	}

	marshaler := cqrs.JSONMarshaler{
		GenerateName: cqrs.StructName,
	}

	eventBus, err := cqrs.NewEventBusWithConfig(redisPublisher, cqrs.EventBusConfig{
		GeneratePublishTopic: func(params cqrs.GenerateEventPublishTopicParams) (string, error) {
			return params.EventName, nil
		},
		Marshaler: marshaler,
		Logger:    logger,
	})
	if err != nil {
		panic(err)
	}

	router := message.NewDefaultRouter(logger)
	router.AddMiddleware(delayedRequeuer.Middleware()...)

	eventProcessor, err := cqrs.NewEventProcessorWithConfig(router, cqrs.EventProcessorConfig{
		GenerateSubscribeTopic: func(params cqrs.EventProcessorGenerateSubscribeTopicParams) (string, error) {
			return params.EventName, nil
		},
		SubscriberConstructor: func(params cqrs.EventProcessorSubscriberConstructorParams) (message.Subscriber, error) {
			return redisstream.NewSubscriber(redisstream.SubscriberConfig{
				Client:        redisClient,
				ConsumerGroup: params.HandlerName,
			}, logger)
		},
		Marshaler: marshaler,
		Logger:    logger,
	})
	if err != nil {
		panic(err)
	}

	err = eventProcessor.AddHandlers(
		cqrs.NewEventHandler(
			"OnOrderPlacedHandler",
			func(ctx context.Context, event *OrderPlaced) error {
				if event.OrderID == "" {
					fmt.Println("ERROR: Received order placed without order_id")
					return fmt.Errorf("empty order_id")
				}

				fmt.Println("Received order placed:", event.OrderID)

				return nil
			},
		),
	)
	if err != nil {
		panic(err)
	}

	go func() {
		err = delayedRequeuer.Run(context.Background())
		if err != nil {
			panic(err)
		}
	}()

	go func() {
		err = router.Run(context.Background())
		if err != nil {
			panic(err)
		}
	}()

	<-router.Running()

	i := 0

	for {
		e := newFakeOrderPlaced()

		i++

		if i == 10 {
			e.OrderID = ""
			i = 0
		}

		err = eventBus.Publish(context.Background(), e)
		if err != nil {
			panic(err)
		}

		time.Sleep(1 * time.Second)
	}
}

func newFakeOrderPlaced() OrderPlaced {
	var products []Product

	for i := 0; i < rand.Intn(5)+1; i++ {
		products = append(products, Product{
			ID:   watermill.NewShortUUID(),
			Name: gofakeit.ProductName(),
		})
	}

	return OrderPlaced{
		OrderID: watermill.NewUUID(),
		Customer: Customer{
			ID:    watermill.NewULID(),
			Name:  gofakeit.Name(),
			Email: gofakeit.Email(),
			Phone: gofakeit.Phone(),
		},
		Address: Address{
			Street:  gofakeit.Street(),
			City:    gofakeit.City(),
			Zip:     gofakeit.Zip(),
			Country: gofakeit.Country(),
		},
		Products: products,
	}
}

type OrderPlaced struct {
	OrderID  string    `json:"order_id"`
	Customer Customer  `json:"customer"`
	Address  Address   `json:"address"`
	Products []Product `json:"products"`
}

type Customer struct {
	ID    string `json:"id"`
	Name  string `json:"name"`
	Email string `json:"email"`
	Phone string `json:"phone"`
}

type Address struct {
	Street  string `json:"street"`
	City    string `json:"city"`
	Zip     string `json:"zip"`
	Country string `json:"country"`
}

type Product struct {
	ID   string `json:"id"`
	Name string `json:"name"`
}
