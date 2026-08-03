// Package events publishes and consumes ConversationEvents on RabbitMQ so
// that every chatbot replica hears about conversation updates and
// termination requests, regardless of which replica actually holds a
// conversation's processing lock or is serving a given SSE subscriber.
package events

import (
	"encoding/json"

	"github.com/Kaese72/chatbot/eventmodels"
	amqp "github.com/rabbitmq/amqp091-go"
)

const conversationEventsExchange = "conversationEvents"

// Publisher publishes ConversationEvents to the conversationEvents fanout
// exchange.
type Publisher struct {
	conn    *amqp.Connection
	channel *amqp.Channel
}

func NewPublisher(connectionString string) (*Publisher, error) {
	conn, err := amqp.Dial(connectionString)
	if err != nil {
		return nil, err
	}
	ch, err := conn.Channel()
	if err != nil {
		conn.Close()
		return nil, err
	}
	if err := ch.ExchangeDeclare(conversationEventsExchange, "fanout", true, false, false, false, nil); err != nil {
		ch.Close()
		conn.Close()
		return nil, err
	}
	return &Publisher{conn: conn, channel: ch}, nil
}

func (p *Publisher) Publish(event eventmodels.ConversationEvent) error {
	body, err := json.Marshal(event)
	if err != nil {
		return err
	}
	return p.channel.Publish(conversationEventsExchange, "", false, false, amqp.Publishing{
		ContentType: "application/json",
		Body:        body,
	})
}

func (p *Publisher) Close() {
	p.channel.Close()
	p.conn.Close()
}
