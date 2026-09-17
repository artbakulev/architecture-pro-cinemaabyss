package main

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"log"
	"net/http"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"github.com/IBM/sarama"
)

const (
	topicMovie    = "movie-events"
	topicUser     = "user-events"
	topicPayment  = "payment-events"
	consumerGroup = "events-service"
	maxBodySize   = 1 << 20
)

type eventResponse struct {
	Status    string          `json:"status"`
	Partition int32           `json:"partition"`
	Offset    int64           `json:"offset"`
	Event     json.RawMessage `json:"event"`
}

type errorResponse struct {
	Error string `json:"error"`
}

type eventsAPI struct {
	producer sarama.SyncProducer
}

func (a *eventsAPI) handleEvent(topic string) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		payload, err := io.ReadAll(http.MaxBytesReader(w, r.Body, maxBodySize))
		if err != nil {
			writeJSON(w, http.StatusBadRequest, errorResponse{Error: "cannot read request body"})
			return
		}
		if !json.Valid(payload) {
			writeJSON(w, http.StatusBadRequest, errorResponse{Error: "request body is not valid json"})
			return
		}

		partition, offset, err := a.producer.SendMessage(&sarama.ProducerMessage{
			Topic: topic,
			Value: sarama.ByteEncoder(payload),
		})
		if err != nil {
			log.Printf("publish to %s: %v", topic, err)
			writeJSON(w, http.StatusInternalServerError, errorResponse{Error: "cannot publish event"})
			return
		}

		log.Printf("produced event to %s partition %d offset %d", topic, partition, offset)
		writeJSON(w, http.StatusCreated, eventResponse{
			Status:    "success",
			Partition: partition,
			Offset:    offset,
			Event:     payload,
		})
	}
}

func handleHealth(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, map[string]bool{"status": true})
}

func writeJSON(w http.ResponseWriter, status int, body any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	if err := json.NewEncoder(w).Encode(body); err != nil {
		log.Printf("write response: %v", err)
	}
}

type loggingConsumer struct{}

func (loggingConsumer) Setup(sarama.ConsumerGroupSession) error   { return nil }
func (loggingConsumer) Cleanup(sarama.ConsumerGroupSession) error { return nil }

func (loggingConsumer) ConsumeClaim(session sarama.ConsumerGroupSession, claim sarama.ConsumerGroupClaim) error {
	for message := range claim.Messages() {
		log.Printf("consumed event from %s partition %d offset %d: %s",
			message.Topic, message.Partition, message.Offset, message.Value)
		session.MarkMessage(message, "")
	}
	return nil
}

func newConfig() *sarama.Config {
	config := sarama.NewConfig()
	config.Producer.Return.Successes = true
	config.Producer.RequiredAcks = sarama.WaitForAll
	config.Consumer.Offsets.Initial = sarama.OffsetOldest
	config.Metadata.Retry.Max = 10
	config.Metadata.Retry.Backoff = 3 * time.Second
	return config
}

func ensureTopics(brokers []string, config *sarama.Config) error {
	admin, err := sarama.NewClusterAdmin(brokers, config)
	if err != nil {
		return err
	}
	defer admin.Close()

	detail := &sarama.TopicDetail{NumPartitions: 3, ReplicationFactor: 1}
	for _, topic := range []string{topicMovie, topicUser, topicPayment} {
		err := admin.CreateTopic(topic, detail, false)
		var topicErr *sarama.TopicError
		if err != nil && !errors.As(err, &topicErr) {
			return err
		}
		if topicErr != nil && topicErr.Err != sarama.ErrTopicAlreadyExists {
			return err
		}
	}
	return nil
}

func waitForKafka(brokers []string, config *sarama.Config) error {
	var err error
	for attempt := 1; attempt <= 30; attempt++ {
		var client sarama.Client
		client, err = sarama.NewClient(brokers, config)
		if err == nil {
			client.Close()
			return nil
		}
		log.Printf("kafka is not ready (attempt %d): %v", attempt, err)
		time.Sleep(3 * time.Second)
	}
	return err
}

func runConsumer(ctx context.Context, brokers []string, config *sarama.Config) {
	group, err := sarama.NewConsumerGroup(brokers, consumerGroup, config)
	if err != nil {
		log.Printf("create consumer group: %v", err)
		return
	}
	defer group.Close()

	topics := []string{topicMovie, topicUser, topicPayment}
	for ctx.Err() == nil {
		if err := group.Consume(ctx, topics, loggingConsumer{}); err != nil {
			log.Printf("consume: %v", err)
			time.Sleep(time.Second)
		}
	}
}

func main() {
	brokers := strings.Split(getEnv("KAFKA_BROKERS", "kafka:9092"), ",")
	config := newConfig()

	if err := waitForKafka(brokers, config); err != nil {
		log.Fatalf("connect to kafka: %v", err)
	}
	if err := ensureTopics(brokers, config); err != nil {
		log.Fatalf("create topics: %v", err)
	}

	producer, err := sarama.NewSyncProducer(brokers, config)
	if err != nil {
		log.Fatalf("create producer: %v", err)
	}
	defer producer.Close()

	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()
	go runConsumer(ctx, brokers, config)

	api := &eventsAPI{producer: producer}
	mux := http.NewServeMux()
	mux.HandleFunc("GET /api/events/health", handleHealth)
	mux.HandleFunc("POST /api/events/movie", api.handleEvent(topicMovie))
	mux.HandleFunc("POST /api/events/user", api.handleEvent(topicUser))
	mux.HandleFunc("POST /api/events/payment", api.handleEvent(topicPayment))

	server := &http.Server{
		Addr:              ":" + getEnv("PORT", "8082"),
		Handler:           mux,
		ReadHeaderTimeout: 5 * time.Second,
	}

	go func() {
		log.Printf("events service listening on %s", server.Addr)
		if err := server.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			log.Fatalf("listen: %v", err)
		}
	}()

	<-ctx.Done()
	log.Println("shutting down")
	shutdownCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := server.Shutdown(shutdownCtx); err != nil {
		log.Printf("shutdown: %v", err)
	}
}

func getEnv(key, fallback string) string {
	if value := os.Getenv(key); value != "" {
		return value
	}
	return fallback
}
