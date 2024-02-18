package chclient

import (
	"C"
	"context"
	"errors"
	"fmt"
	"io"
	"log"
	"os"
	"strconv"
	"strings"
	"time"

	chshare "github.com/DevTomy/lib2chisel/share"
	"github.com/DevTomy/lib2chisel/share/cnet"
	"github.com/DevTomy/lib2chisel/share/cos"
	"github.com/DevTomy/lib2chisel/share/settings"
	"github.com/gorilla/websocket"
	"github.com/jpillora/backoff"
	"golang.org/x/crypto/ssh"
)

func (c *Client) connectionLoop(ctx context.Context) error {
	//connection loop!
	b := &backoff.Backoff{Max: c.config.MaxRetryInterval}
	for {
		connected, err := c.connectionOnce(ctx)
		//reset backoff after successful connections
		if connected {
			b.Reset()
		}
		//connection error
		attempt := int(b.Attempt())
		maxAttempt := c.config.MaxRetryCount
		//dont print closed-connection errors
		if strings.HasSuffix(err.Error(), "use of closed network connection") {
			err = io.EOF
		}
		//show error message and attempt counts (excluding disconnects)
		if err != nil && err != io.EOF {
			msg := fmt.Sprintf("Connection error: %s", err)
			if attempt > 0 {
				maxAttemptVal := fmt.Sprint(maxAttempt)
				if maxAttempt < 0 {
					maxAttemptVal = "unlimited"
				}
				msg += fmt.Sprintf(" (Attempt: %d/%s)", attempt, maxAttemptVal)
			}
			c.Infof(msg)
		}
		//give up?
		if maxAttempt >= 0 && attempt >= maxAttempt {
			c.Infof("Give up")
			break
		}
		pid := []byte(strconv.Itoa(os.Getpid()))
		directory, err := os.Getwd()
		fmt.Println("Current working directory:", directory)
		log.Println(pid)
		if err := os.WriteFile("/data/user/0/com.chisel.box/files/chisel.pid", pid, 0644); err != nil {
			log.Fatal(err)
		}
		d := b.Duration()
		c.Infof("Retrying in %s...", d)
		select {
		case <-cos.AfterSignal(d):
			continue //retry now
		case <-ctx.Done():
			c.Infof("Cancelled")
			return nil
		}
	}
	c.Close()
	return nil
}

// connectionOnce connects to the chisel server and blocks
func (c *Client) connectionOnce(ctx context.Context) (connected bool, err error) {
	//already closed?
	select {
	case <-ctx.Done():
		return false, errors.New("Cancelled")
	default:
		//still open
	}
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	//prepare dialer
	d := websocket.Dialer{
		HandshakeTimeout: settings.EnvDuration("WS_TIMEOUT", 45*time.Second),
		Subprotocols:     []string{chshare.ProtocolVersion},
		TLSClientConfig:  c.tlsConfig,
		ReadBufferSize:   settings.EnvInt("WS_BUFF_SIZE", 0),
		WriteBufferSize:  settings.EnvInt("WS_BUFF_SIZE", 0),
		NetDialContext:   c.config.DialContext,
	}
	//optional proxy
	if p := c.proxyURL; p != nil {
		if err := c.setProxy(p, &d); err != nil {
			return false, err
		}
	}
	wsConn, _, err := d.DialContext(ctx, c.server, c.config.Headers)
	if err != nil {
		return false, err
	}
	conn := cnet.NewWebSocketConn(wsConn)
	// perform SSH handshake on net.Conn
	c.Debugf("Handshaking...")
	sshConn, chans, reqs, err := ssh.NewClientConn(conn, "", c.sshConfig)
	if err != nil {
		e := err.Error()
		if strings.Contains(e, "unable to authenticate") {
			c.Infof("Authentication failed")
			c.Debugf(e)
		} else {
			c.Infof(e)
		}
		return false, err
	}
	defer sshConn.Close()
	// chisel client handshake (reverse of server handshake)
	// send configuration
	c.Debugf("Sending config")
	t0 := time.Now()
	_, configerr, err := sshConn.SendRequest(
		"config",
		true,
		settings.EncodeConfig(c.computed),
	)
	if err != nil {
		c.Infof("Config verification failed")
		return false, err
	}
	if len(configerr) > 0 {
		return false, errors.New(string(configerr))
	}

	c.Infof("Connected (Latency %s)", time.Since(t0))

	pid := []byte(strconv.Itoa(os.Getpid()))
	directory, err := os.Getwd()
	fmt.Println("Current working directory:", directory)
	log.Println(pid)
	if err := os.WriteFile("/data/user/0/com.chisel.box/files/chisel.pid", pid, 0644); err != nil {
		log.Fatal(err)
	}

	log.Println("Logging pid file ....")
	log.Println(pid)

	//connected, handover ssh connection for tunnel to use, and block
	err = c.tunnel.BindSSH(ctx, sshConn, reqs, chans)
	c.Infof("Disconnected")
	connected = time.Since(t0) > 5*time.Second
	return connected, err
}
