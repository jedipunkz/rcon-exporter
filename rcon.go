package main

import (
	"bytes"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"net"
	"time"
)

// Source RCON protocol as implemented by Minecraft Java Edition:
// little-endian int32 length, id, type, then payload + 2 NUL bytes.
const (
	typeResponse = 0
	typeCommand  = 2
	typeLogin    = 3
	// Unknown type: the server answers "Unknown request" with the same id.
	// Used as an end-of-response marker when a response fills a whole packet.
	typeSentinel = 100

	maxPayload    = 4096 // server splits longer responses into several packets
	maxPacketSize = maxPayload + 10
)

type rconClient struct {
	conn net.Conn
	id   int32
}

func dialRCON(addr, password string, timeout time.Duration) (*rconClient, error) {
	conn, err := net.DialTimeout("tcp", addr, timeout)
	if err != nil {
		return nil, err
	}
	// One deadline for the whole session (login + every command of a scrape).
	if err := conn.SetDeadline(time.Now().Add(timeout)); err != nil {
		conn.Close()
		return nil, err
	}
	c := &rconClient{conn: conn}
	if err := c.write(typeLogin, password); err != nil {
		conn.Close()
		return nil, err
	}
	id, _, _, err := c.read()
	if err != nil {
		conn.Close()
		return nil, err
	}
	if id == -1 {
		conn.Close()
		return nil, errors.New("rcon authentication failed")
	}
	return c, nil
}

func (c *rconClient) Close() error { return c.conn.Close() }

// Run executes one command and returns the full (possibly multi-packet) response.
func (c *rconClient) Run(cmd string) (string, error) {
	if err := c.write(typeCommand, cmd); err != nil {
		return "", err
	}
	cmdID := c.id
	// The vanilla server parses only one packet per socket read and drops the
	// connection if packets arrive coalesced, so the sentinel is sent only after
	// a full-size packet shows the response may continue.
	var sentinelID int32
	var out bytes.Buffer
	for {
		id, _, body, err := c.read()
		if err != nil {
			return "", err
		}
		switch {
		case id == cmdID:
			out.Write(body)
			if len(body) < maxPayload && sentinelID == 0 {
				return out.String(), nil
			}
			if sentinelID == 0 {
				if err := c.write(typeSentinel, ""); err != nil {
					return "", err
				}
				sentinelID = c.id
			}
		case id == sentinelID && sentinelID != 0:
			return out.String(), nil
		case id == -1:
			return "", errors.New("rcon: not authenticated")
		default:
			return "", fmt.Errorf("rcon: unexpected packet id %d", id)
		}
	}
}

func (c *rconClient) write(typ int32, payload string) error {
	c.id++
	buf := make([]byte, 0, 14+len(payload))
	buf = binary.LittleEndian.AppendUint32(buf, uint32(10+len(payload)))
	buf = binary.LittleEndian.AppendUint32(buf, uint32(c.id))
	buf = binary.LittleEndian.AppendUint32(buf, uint32(typ))
	buf = append(buf, payload...)
	buf = append(buf, 0, 0)
	_, err := c.conn.Write(buf)
	return err
}

func (c *rconClient) read() (id, typ int32, body []byte, err error) {
	var size int32
	if err = binary.Read(c.conn, binary.LittleEndian, &size); err != nil {
		return
	}
	if size < 10 || size > maxPacketSize {
		return 0, 0, nil, fmt.Errorf("rcon: invalid packet size %d", size)
	}
	buf := make([]byte, size)
	if _, err = io.ReadFull(c.conn, buf); err != nil {
		return
	}
	id = int32(binary.LittleEndian.Uint32(buf[0:4]))
	typ = int32(binary.LittleEndian.Uint32(buf[4:8]))
	return id, typ, buf[8 : size-2], nil
}
