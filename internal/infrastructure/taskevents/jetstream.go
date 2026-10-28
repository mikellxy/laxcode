// Package taskevents owns a per-workplace embedded JetStream. It opens no network
// listener; the cloud's existing exclusive workplace ownership also owns its files.
package taskevents

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	ns "github.com/nats-io/nats-server/v2/server"
	"github.com/nats-io/nats.go"
)

const stream = "TASK_EVENTS"

type Event struct {
	TaskID     string          `json:"task_id"`
	EventID    uint64          `json:"event_id"`
	MessageSeq uint64          `json:"message_seq,omitempty"`
	Type       string          `json:"type"`
	Data       json.RawMessage `json:"data"`
}
type Log struct {
	server *ns.Server
	conn   *nats.Conn
	js     nats.JetStreamContext
}

func Open(dir string) (*Log, error) {
	server, err := ns.NewServer(&ns.Options{DontListen: true, NoSigs: true, NoLog: true, JetStream: true, StoreDir: dir, SyncAlways: true})
	if err != nil {
		return nil, err
	}
	server.Start()
	conn, err := nats.Connect("", nats.InProcessServer(server))
	if err != nil {
		server.Shutdown()
		return nil, err
	}
	js, err := conn.JetStream()
	if err == nil {
		_, err = js.AddStream(&nats.StreamConfig{Name: stream, Subjects: []string{"tasks.*"}, Storage: nats.FileStorage, Retention: nats.LimitsPolicy})
	}
	if err != nil {
		conn.Close()
		server.Shutdown()
		return nil, err
	}
	return &Log{server: server, conn: conn, js: js}, nil
}
func (l *Log) Close()          { l.conn.Close(); l.server.Shutdown(); l.server.WaitForShutdown() }
func subject(id string) string { return "tasks." + id }
func (l *Log) Publish(ctx context.Context, e Event) error {
	body, err := json.Marshal(e)
	if err != nil {
		return err
	}
	_, err = l.js.Publish(subject(e.TaskID), body, nats.Context(ctx), nats.MsgId(fmt.Sprintf("%s:%d", e.TaskID, e.EventID)))
	return err
}
func (l *Log) Last(ctx context.Context, id string) (uint64, error) {
	msg, err := l.js.GetLastMsg(stream, subject(id), nats.Context(ctx))
	if errors.Is(err, nats.ErrMsgNotFound) {
		return 0, nil
	}
	if err != nil {
		return 0, err
	}
	var e Event
	err = json.Unmarshal(msg.Data, &e)
	return e.EventID, err
}
func (l *Log) Purge(ctx context.Context, id string) error {
	return l.js.PurgeStream(stream, &nats.StreamPurgeRequest{Subject: subject(id)}, nats.Context(ctx))
}

type Subscription struct{ sub *nats.Subscription }

func (l *Log) Subscribe(ctx context.Context, id string) (*Subscription, error) {
	sub, err := l.js.SubscribeSync(subject(id), nats.BindStream(stream), nats.DeliverAll(), nats.OrderedConsumer(), nats.Context(ctx))
	if err != nil {
		return nil, err
	}
	return &Subscription{sub}, nil
}
func (s *Subscription) Close() { _ = s.sub.Unsubscribe() }

// Next has a bounded wait so the HTTP handler can poll task state in the same loop.
func (s *Subscription) Next(ctx context.Context) (Event, error) {
	ctx, cancel := context.WithTimeout(ctx, 250*time.Millisecond)
	defer cancel()
	msg, err := s.sub.NextMsgWithContext(ctx)
	if err != nil {
		return Event{}, err
	}
	var e Event
	err = json.Unmarshal(msg.Data, &e)
	return e, err
}
