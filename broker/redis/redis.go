package nats

import (
	"encoding/json"
	"errors"
	"net/url"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"time"

	log "github.com/Sirupsen/logrus"
	"github.com/gomodule/redigo/redis"
	"github.com/tatataufik/gocelery/broker"
	// redis broker
)

const (
	// TaskEventChannel for task event pubsub
	TaskEventChannel string = "gocelerytaskevent"
)

var pathDBRegexp = regexp.MustCompile(`/(\d*)\z`)

// Broker implements Nats broker
type Broker struct {
	sync.Mutex
	redisURL string

	pool *redis.Pool
}

//
func init() {
	// register nats
	broker.Register("redis", &Broker{})
}

// Connect to rabbitmq
func (b *Broker) Connect(uri string) error {
	s := strings.SplitN(uri, "://", 2)

	if len(s) < 2 || s[1] == "" {
		return errors.New("Invalid redis URL")
	}

	b.redisURL = uri
	conn, err := redis.DialURL(uri)
	defer conn.Close()
	if err != nil {
		return err
	}

	// dial the server
	b.pool = &redis.Pool{
		MaxIdle: 3,
		Dial:    b.dial,
		TestOnBorrow: func(c redis.Conn, t time.Time) error {
			_, err := c.Do("PING")
			return err
		},
	}

	log.Debug("Connected to redis")
	return nil
}

// Close the broker and cleans up resources
func (b *Broker) Close() error {
	log.Debug("Closing broker: ", b.redisURL)
	b.pool.Close()
	return nil
}

func (b *Broker) dial() (redis.Conn, error) {
	c, err := redis.DialURL(b.redisURL)
	if err != nil {
		return nil, err
	}

	u, err := url.Parse(b.redisURL)
	if err != nil {
		return nil, err
	}

	match := pathDBRegexp.FindStringSubmatch(u.Path)
	if len(match) == 2 {
		db := 0
		if len(match[1]) > 0 {
			db, _ = strconv.Atoi(match[1])

		}
		log.Debug("Select db=", db)
		if _, err := c.Do("SELECT", db); err != nil {

		}
	}

	return c, err
}

// GetTasks waits and fetches the tasks from queue
func (b *Broker) GetTasks(queue string) <-chan *broker.Message {
	msg := make(chan *broker.Message)

	// fetch messages
	log.Infof("Waiting for tasks at: %s, queue: %s", b.redisURL, queue)
	conn := b.pool.Get()
	psc := redis.PubSubConn{Conn: conn}

	psc.Subscribe(queue)
	go func() {
		for {
			switch v := psc.Receive().(type) {
			case redis.Message:
				m := &broker.Message{}
				json.Unmarshal(v.Data, &m)
				msg <- m
			}
		}
	}()
	return msg
}

// GetTaskResult fetchs task result for the specified taskID
func (b *Broker) GetTaskResult(taskID string) <-chan *broker.Message {
	msg := make(chan *broker.Message)

	// fetch messages
	log.Debug("Waiting for Task Result Messages: ", taskID)
	conn := b.pool.Get()
	psc := redis.PubSubConn{Conn: conn}
	psc.Subscribe(taskID)

	go func() {
		for {
			switch v := psc.Receive().(type) {
			case redis.Message:
				log.Info("message: ", string(v.Data))
				m := &broker.Message{}
				err := json.Unmarshal(v.Data, &m)
				if err != nil {
					log.Error("Failed to unmarshal message.")
				} else {
					log.Debug("Task Result message: ", string(m.Body))
					msg <- m
				}
				psc.Unsubscribe()
				conn.Close()
				close(msg)
				break
			}
		}
	}()
	log.Debug("Subscribed to Task Result")
	return msg
}

// PublishTask sends a task to queue
func (b *Broker) PublishTask(queueName, key string, message *broker.Message, ignoreResults bool) error {
	bytes, err := json.Marshal(message)
	if err != nil {
		log.Error("Failed to marshal message: ", err)
		return err
	}
	conn := b.pool.Get()
	defer conn.Close()
	_, err = conn.Do("PUBLISH", queueName, bytes)
	if err != nil {
		log.Error("Failed to publish message: ", err)
		return err
	}

	log.Debug("Published Task to queue: ", key)
	return nil
}

// PublishTaskResult sends task result back to task queue
func (b *Broker) PublishTaskResult(key string, message *broker.Message) error {
	log.Debug("Publishing Task Result:", key)
	bytes, err := json.Marshal(message)
	if err != nil {
		log.Error("Failed to marshal message: ", err)
		return err
	}
	conn := b.pool.Get()
	defer conn.Close()
	_, err = conn.Do("PUBLISH", key, bytes)
	return err
}

// PublishTaskEvent sends task events back to event queue
func (b *Broker) PublishTaskEvent(key string, message *broker.Message) error {
	bytes, err := json.Marshal(message)
	if err != nil {
		log.Error("Failed to marshal message: ", err)
		return err
	}
	conn := b.pool.Get()
	defer conn.Close()
	_, err = conn.Do("PUBLISH", TaskEventChannel, bytes)
	return err
}
