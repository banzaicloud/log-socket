package internal

import (
	"crypto/tls"
	"errors"
	"net/http"
	"strings"

	"github.com/gorilla/websocket"
	"k8s.io/apimachinery/pkg/types"

	"github.com/banzaicloud/log-socket/log"
)

func Listen(addr string, tlsConfig *tls.Config, reg ListenerRegistry, logs log.Sink, stopSignal Handleable, terminationSignal Handleable) {
	upgrader := websocket.Upgrader{}
	server := &http.Server{
		Addr: addr,
		Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			log.Event(logs, "new listener", log.V(1), log.Fields{"req": r})

			// request:
			// * token
			// * (cluster)flow name (+ namespace)

			// TODO: auth (token review)

			wsConn, err := upgrader.Upgrade(w, r, nil)
			if err != nil {
				log.Event(logs, "failed to upgrade connection", log.Error(err))
				return
			}

			log.Event(logs, "successful websocket upgrade", log.V(1), log.Fields{"req": r, "wsConn": wsConn})

			// TODO: create (cluster)output (if not exists) and add it to (cluster)flow

			nn, err := ExtractFlow(r)
			if err != nil {
				http.Error(w, err.Error(), http.StatusNotFound)
				return
			}

			l := listener{
				Conn: wsConn,
				reg:  reg,
				logs: logs,
				flow: nn,
				// TODO: add auth info
			}
			reg.Register(l)
			wsConn.SetCloseHandler(func(code int, text string) error {
				log.Event(logs, "websocket connection closing", log.Fields{"code": code, "text": text})
				reg.Unregister(l)
				return nil
			})
		}),
		TLSConfig: tlsConfig,
	}

	if err := server.ListenAndServeTLS("", ""); err != nil && err != http.ErrServerClosed {
		log.Event(logs, "websocket listener server returned an error", log.Error(err))
	}
}

type ListenerRegistry interface {
	Register(Listener)
	Unregister(Listener)
}

type Listener interface {
	Send(Record)
}

type listener struct {
	Conn *websocket.Conn
	reg  ListenerRegistry
	logs log.Sink
	flow types.NamespacedName
}

func (l listener) Equals(o listener) bool {
	return l.Conn == o.Conn
}

func (l listener) Send(r Record) {
	// TODO: check auth

	wc, err := l.Conn.NextWriter(websocket.BinaryMessage)
	if err != nil {
		log.Event(l.logs, "an error occurred while getting next writer for websocket connection", log.Error(err))
		goto unregister
	}
	if _, err := wc.Write(r.Data); err != nil {
		log.Event(l.logs, "an error occurred while writing record data to websocket connection", log.Error(err))
		goto unregister
	}
	if err := wc.Close(); err != nil {
		log.Event(l.logs, "an error occurred while flushing frame to websocket connection", log.Error(err))
		goto unregister
	}

	return

unregister:
	go l.reg.Unregister(l)
}

func ExtractFlow(req *http.Request) (res types.NamespacedName, err error) {
	elts := strings.Split(strings.TrimLeft(req.URL.Path, "/"), "/")
	if len(elts) < 2 {
		return res, errors.New("error parsing listener reg URL")
	}

	switch elts[0] {
	case "flow":
		if len(elts) != 3 {
			return res, errors.New("error parsing listener reg URL")
		}
		res = types.NamespacedName{Namespace: elts[1], Name: elts[2]}
	case "clusterflow":
		res = types.NamespacedName{Name: elts[1]}
	default:
		return res, errors.New("unknown flow type in listener reg URL")
	}
	return
}
