package events

import (
	"context"
	"encoding/json"

	"github.com/Kaese72/chatbot/eventmodels"
	log "github.com/Kaese72/huemie-lib/logging"
	amqp "github.com/rabbitmq/amqp091-go"
)

// Handler processes a ConversationEvent picked up from RabbitMQ. It must
// return promptly, since it runs synchronously on the consumer's single
// delivery loop.
type Handler interface {
	HandleConversationEvent(eventmodels.ConversationEvent)
}

// StartConsumer connects to RabbitMQ and subscribes to the
// conversationEvents fanout exchange via an anonymous, auto-delete,
// exclusive queue — so every replica gets its own copy of every event, per
// the README's "we send a RabbitMQ message which every single replica
// picks up" requirement for termination. It runs until ctx is cancelled.
func StartConsumer(ctx context.Context, connectionString string, handler Handler) error {
	conn, err := amqp.Dial(connectionString)
	if err != nil {
		return err
	}

	ch, err := conn.Channel()
	if err != nil {
		conn.Close()
		return err
	}

	if err := ch.ExchangeDeclare(conversationEventsExchange, "fanout", true, false, false, false, nil); err != nil {
		ch.Close()
		conn.Close()
		return err
	}

	q, err := ch.QueueDeclare("", false, false, true, false, nil)
	if err != nil {
		ch.Close()
		conn.Close()
		return err
	}

	if err := ch.QueueBind(q.Name, "", conversationEventsExchange, false, nil); err != nil {
		ch.Close()
		conn.Close()
		return err
	}

	msgs, err := ch.Consume(q.Name, "", true, false, false, false, nil)
	if err != nil {
		ch.Close()
		conn.Close()
		return err
	}

	go func() {
		defer ch.Close()
		defer conn.Close()
		for {
			select {
			case <-ctx.Done():
				return
			case msg, ok := <-msgs:
				if !ok {
					return
				}
				var event eventmodels.ConversationEvent
				if err := json.Unmarshal(msg.Body, &event); err != nil {
					log.Error("failed to unmarshal conversation event: "+err.Error(), map[string]interface{}{})
					continue
				}
				handler.HandleConversationEvent(event)
			}
		}
	}()

	return nil
}
