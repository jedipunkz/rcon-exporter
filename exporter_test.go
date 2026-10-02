package main

import (
	"encoding/binary"
	"net"
	"regexp"
	"strings"
	"testing"
	"time"
)

// Outputs captured from a vanilla 26.3 server, except the list line with players,
// which follows the vanilla "commands.list.nameAndId" format.
func TestParse(t *testing.T) {
	online, max, players, ok := parseList("There are 2 of a max of 20 players online: Steve (069a79f4-44e9-4726-a5be-fca90e38aaf5), Alex_1 (853c80ef-3c37-49fd-aa49-938b674adae6)")
	if !ok || online != 2 || max != 20 || len(players) != 2 || players[1] != (player{"Alex_1", "853c80ef-3c37-49fd-aa49-938b674adae6"}) {
		t.Fatalf("parseList = %v %v %v %v", online, max, players, ok)
	}
	if _, _, players, ok := parseList("There are 0 of a max of 20 players online: "); !ok || players != nil {
		t.Fatalf("empty list = %v %v", players, ok)
	}

	tick := "The game is running normallyTarget tick rate: 20.0 per second.\nAverage time per tick: 2.2ms (Target: 50.0ms)Percentiles: P50: 2.0ms P95: 4.0ms P99: 5.3ms. Sample: 100"
	for _, tc := range []struct {
		re   *regexp.Regexp
		in   string
		want float64
	}{
		{tickTargetRe, tick, 20},
		{tickAvgRe, tick, 2.2},
		{gameTimeRe, "The game time is 1199 tick(s)", 1199},
		{entityCntRe, "Test passed. Count: 4", 4},
		{borderRe, "The world border is currently 59999968 block(s) wide", 59999968},
		{entityDataRe, "Steve has the following entity data: 19.5f", 19.5},
	} {
		if v, ok := parseFloat(tc.re, tc.in); !ok || v != tc.want {
			t.Errorf("%s on %q = %v %v, want %v", tc.re, tc.in, v, ok, tc.want)
		}
	}
	if got := tickPctRe.FindAllStringSubmatch(tick, -1); len(got) != 3 || got[2][2] != "5.3" {
		t.Errorf("percentiles = %v", got)
	}
	if m := versionRe.FindStringSubmatch("Server version info:id = 26.3name = 26.3data = 5023series = mainprotocol = 777 (0x309)build_time = x"); m == nil || m[1] != "26.3" || m[2] != "777" {
		t.Errorf("version = %v", m)
	}
}

// Fake server that splits a long response into 4096-byte packets and closes the
// connection when two packets arrive in one read, like the vanilla server.
func TestRunMultiPacket(t *testing.T) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()
	long := strings.Repeat("x", 4096*2) // exact multiple: needs the sentinel
	go func() {
		conn, err := ln.Accept()
		if err != nil {
			return
		}
		defer conn.Close()
		send := func(id int32, body string) {
			b := binary.LittleEndian.AppendUint32(nil, uint32(10+len(body)))
			b = binary.LittleEndian.AppendUint32(b, uint32(id))
			b = binary.LittleEndian.AppendUint32(b, 0)
			conn.Write(append(append(b, body...), 0, 0))
		}
		buf := make([]byte, 1460)
		for {
			n, err := conn.Read(buf)
			if err != nil {
				return
			}
			size := int(binary.LittleEndian.Uint32(buf))
			if size != n-4 {
				return // coalesced packets
			}
			id := int32(binary.LittleEndian.Uint32(buf[4:]))
			switch typ := binary.LittleEndian.Uint32(buf[8:]); {
			case typ == typeLogin:
				send(id, "")
			case typ == typeCommand && string(buf[12:n-2]) == "long":
				for i := 0; i < len(long); i += 4096 {
					send(id, long[i:i+4096])
				}
			case typ == typeCommand:
				send(id, "short")
			default:
				send(id, "Unknown request 64")
			}
		}
	}()

	c, err := dialRCON(ln.Addr().String(), "pw", 2*time.Second)
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	for _, tc := range []struct{ cmd, want string }{{"short", "short"}, {"long", long}, {"short", "short"}} {
		if got, err := c.Run(tc.cmd); err != nil || got != tc.want {
			t.Fatalf("Run(%q) = %d bytes, %v", tc.cmd, len(got), err)
		}
	}
}
