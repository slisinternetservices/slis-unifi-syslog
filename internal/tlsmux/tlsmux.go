// Package tlsmux serves plain HTTP and HTTPS on one port: the first byte of
// a connection tells them apart (0x16 starts a TLS handshake). The web
// interface keeps working on http://host:8080 while probes and browsers can
// use https://host:8080.
package tlsmux

import (
	"bufio"
	"net"
	"sync"
	"time"
)

// Split returns two listeners fed from ln: one for plain connections and
// one for TLS connections (still to be wrapped with tls.NewListener).
// Closing either closes ln.
func Split(ln net.Listener) (plain, secure net.Listener) {
	p := &sub{parent: ln, ch: make(chan net.Conn), done: make(chan struct{})}
	s := &sub{parent: ln, ch: make(chan net.Conn), done: p.done, once: &p.closeOnce}
	p.once = &p.closeOnce
	go func() {
		for {
			c, err := ln.Accept()
			if err != nil {
				if ne, ok := err.(net.Error); ok && ne.Timeout() {
					time.Sleep(50 * time.Millisecond)
					continue
				}
				p.close()
				return
			}
			go route(c, p, s)
		}
	}()
	return p, s
}

func route(c net.Conn, plain, secure *sub) {
	c.SetReadDeadline(time.Now().Add(15 * time.Second))
	br := bufio.NewReader(c)
	b, err := br.Peek(1)
	c.SetReadDeadline(time.Time{})
	if err != nil {
		c.Close()
		return
	}
	pc := &peeked{Conn: c, r: br}
	to := plain
	if b[0] == 0x16 {
		to = secure
	}
	select {
	case to.ch <- pc:
	case <-to.done:
		c.Close()
	}
}

type peeked struct {
	net.Conn
	r *bufio.Reader
}

func (p *peeked) Read(b []byte) (int, error) { return p.r.Read(b) }

type sub struct {
	parent    net.Listener
	ch        chan net.Conn
	done      chan struct{}
	once      *sync.Once
	closeOnce sync.Once
}

func (s *sub) Accept() (net.Conn, error) {
	select {
	case c := <-s.ch:
		return c, nil
	case <-s.done:
		return nil, net.ErrClosed
	}
}

func (s *sub) close() {
	s.once.Do(func() {
		close(s.done)
		s.parent.Close()
	})
}

func (s *sub) Close() error   { s.close(); return nil }
func (s *sub) Addr() net.Addr { return s.parent.Addr() }
