package main

import (
	"log/slog"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/prometheus/client_golang/prometheus"
)

func desc(name, help string, labels ...string) *prometheus.Desc {
	return prometheus.NewDesc("minecraft_"+name, help, labels, nil)
}

var (
	upDesc            = desc("up", "Whether the RCON connection and login succeeded.")
	playersOnlineDesc = desc("players_online", "Number of online players.")
	playersMaxDesc    = desc("players_max", "Maximum number of players.")
	playerOnlineDesc  = desc("player_online", "Online player. Value is always 1.", "player", "uuid")
	playerHealthDesc  = desc("player_health", "Player health points (20 = full).", "player", "uuid")
	playerFoodDesc    = desc("player_food_level", "Player food level (20 = full).", "player", "uuid")
	playerXPDesc      = desc("player_xp_level", "Player experience level.", "player", "uuid")
	tickTargetDesc    = desc("tick_rate_target", "Target ticks per second (tick query, 1.20.3+).")
	tickAvgDesc       = desc("tick_duration_average_seconds", "Average time per tick (tick query, 1.20.3+).")
	tickPctDesc       = desc("tick_duration_seconds", "Time per tick percentiles over the last samples (tick query, 1.20.3+).", "quantile")
	gameTimeDesc      = desc("game_time_ticks", "Total game ticks elapsed in the world.")
	entitiesDesc      = desc("entities", "Number of entities in loaded chunks.", "dimension")
	borderDesc        = desc("world_border_diameter_blocks", "World border diameter in blocks.")
	difficultyDesc    = desc("difficulty", "Current difficulty. Value is always 1.", "difficulty")
	versionDesc       = desc("version_info", "Server version (version command, 1.21.6+). Value is always 1.", "version", "protocol")
)

var dimensions = []string{"minecraft:overworld", "minecraft:the_nether", "minecraft:the_end"}

var (
	formatCodeRe = regexp.MustCompile(`§.`)
	listRe       = regexp.MustCompile(`There are (\d+) of a max of (\d+) players online:\s*(.*)`)
	listEntryRe  = regexp.MustCompile(`^(\S+) \(([0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12})\)$`)
	tickTargetRe = regexp.MustCompile(`Target tick rate: ([\d.]+)`)
	tickAvgRe    = regexp.MustCompile(`Average time per tick: ([\d.]+)ms`)
	tickPctRe    = regexp.MustCompile(`P(50|95|99): ([\d.]+)ms`)
	gameTimeRe   = regexp.MustCompile(`time is (\d+)`)
	entityDataRe = regexp.MustCompile(`has the following entity data: (-?[\d.]+)`)
	entityCntRe  = regexp.MustCompile(`Test passed\. Count: (\d+)`)
	borderRe     = regexp.MustCompile(`currently ([\d.]+) block`)
	difficultyRe = regexp.MustCompile(`The difficulty is (\w+)`)
	versionRe    = regexp.MustCompile(`id = (.+?)name = .*protocol = (\d+)`)
)

type player struct{ name, uuid string }

type collector struct {
	addr, password string
	timeout        time.Duration
	logger         *slog.Logger
}

func (c *collector) Describe(ch chan<- *prometheus.Desc) {
	for _, d := range []*prometheus.Desc{upDesc, playersOnlineDesc, playersMaxDesc, playerOnlineDesc,
		playerHealthDesc, playerFoodDesc, playerXPDesc, tickTargetDesc, tickAvgDesc, tickPctDesc,
		gameTimeDesc, entitiesDesc, borderDesc, difficultyDesc, versionDesc} {
		ch <- d
	}
}

func (c *collector) Collect(ch chan<- prometheus.Metric) {
	gauge := func(d *prometheus.Desc, v float64, labels ...string) {
		ch <- prometheus.MustNewConstMetric(d, prometheus.GaugeValue, v, labels...)
	}

	conn, err := dialRCON(c.addr, c.password, c.timeout)
	if err != nil {
		c.logger.Error("rcon connect failed", "err", err)
		gauge(upDesc, 0)
		return
	}
	defer conn.Close()
	gauge(upDesc, 1)

	// run returns "" on failure; a missing metric is the signal, so parse misses
	// (older server versions, unknown commands) are only logged at debug level.
	run := func(cmd string) string {
		out, err := conn.Run(cmd)
		if err != nil {
			c.logger.Error("rcon command failed", "cmd", cmd, "err", err)
			return ""
		}
		c.logger.Debug("rcon", "cmd", cmd, "out", out)
		return formatCodeRe.ReplaceAllString(out, "")
	}

	if online, max, players, ok := parseList(run("list uuids")); ok {
		gauge(playersOnlineDesc, online)
		gauge(playersMaxDesc, max)
		for _, p := range players {
			gauge(playerOnlineDesc, 1, p.name, p.uuid)
			// The UUID (validated by listEntryRe) is the selector, so no
			// server-supplied text is interpolated into the command.
			for _, m := range []struct {
				d    *prometheus.Desc
				path string
			}{{playerHealthDesc, "Health"}, {playerFoodDesc, "foodLevel"}, {playerXPDesc, "XpLevel"}} {
				if v, ok := parseFloat(entityDataRe, run("data get entity "+p.uuid+" "+m.path)); ok {
					gauge(m.d, v, p.name, p.uuid)
				}
			}
		}
	}

	tick := run("tick query")
	if v, ok := parseFloat(tickTargetRe, tick); ok {
		gauge(tickTargetDesc, v)
	}
	if v, ok := parseFloat(tickAvgRe, tick); ok {
		gauge(tickAvgDesc, v/1000)
	}
	for _, m := range tickPctRe.FindAllStringSubmatch(tick, -1) {
		v, _ := strconv.ParseFloat(m[2], 64)
		q, _ := strconv.ParseFloat(m[1], 64)
		gauge(tickPctDesc, v/1000, strconv.FormatFloat(q/100, 'g', -1, 64))
	}

	if v, ok := parseFloat(gameTimeRe, run("time query gametime")); ok {
		gauge(gameTimeDesc, v)
	}
	for _, dim := range dimensions {
		// distance=0.. limits the selector to the dimension set by "execute in".
		out := run("execute in " + dim + " if entity @e[distance=0..]")
		if v, ok := parseFloat(entityCntRe, out); ok {
			gauge(entitiesDesc, v, dim)
		} else if out == "Test failed" {
			gauge(entitiesDesc, 0, dim)
		}
	}
	if v, ok := parseFloat(borderRe, run("worldborder get")); ok {
		gauge(borderDesc, v)
	}
	if m := difficultyRe.FindStringSubmatch(run("difficulty")); m != nil {
		gauge(difficultyDesc, 1, strings.ToLower(m[1]))
	}
	if m := versionRe.FindStringSubmatch(run("version")); m != nil {
		gauge(versionDesc, 1, m[1], m[2])
	}
}

// parseList parses "list uuids" output, e.g.
// "There are 1 of a max of 20 players online: Steve (069a79f4-44e9-4726-a5be-fca90e38aaf5)".
func parseList(s string) (online, max float64, players []player, ok bool) {
	m := listRe.FindStringSubmatch(s)
	if m == nil {
		return 0, 0, nil, false
	}
	online, _ = strconv.ParseFloat(m[1], 64)
	max, _ = strconv.ParseFloat(m[2], 64)
	if m[3] == "" {
		return online, max, nil, true
	}
	for _, e := range strings.Split(m[3], ", ") {
		if pm := listEntryRe.FindStringSubmatch(e); pm != nil {
			players = append(players, player{pm[1], pm[2]})
		}
	}
	return online, max, players, true
}

// parseFloat returns the first capture group of re as a float.
func parseFloat(re *regexp.Regexp, s string) (float64, bool) {
	m := re.FindStringSubmatch(s)
	if m == nil {
		return 0, false
	}
	v, err := strconv.ParseFloat(m[1], 64)
	return v, err == nil
}
