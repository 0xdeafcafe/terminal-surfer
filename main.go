package main

import (
	"fmt"
	"math/rand"
	"os"
	"os/signal"
	"sync"
	"syscall"
	"time"

	"golang.org/x/term"
)

const (
	targetFPS    = 20
	numLanes     = 3
	laneWidth    = 7
	trackWidth   = numLanes*laneWidth + 4 // 3 lanes + borders
	farZ           = 20
	spawnZ         = farZ - 1
	dodgeLookahead = 8
)

// --- Game state ---

type obstacle struct {
	lane   int
	z      float64
	active bool
}

type coinObj struct {
	lane   int
	z      float64
	active bool
}

type game struct {
	width, height int
	speed         float64
	score         int
	coins         int
	runnerLane    int
	targetLane    int
	laneX         float64 // smooth interpolation
	obstacles     [20]obstacle
	coinPool      [30]coinObj
	scrollOff     float64
	elapsed       float64
	spawnTimer    float64
	coinTimer     float64
	frame         []byte
}

func newGame(w, h int) *game {
	g := &game{
		width:      w,
		height:     h,
		speed:      6.0,
		runnerLane: 1,
		targetLane: 1,
		laneX:      1.0,
	}
	return g
}

func (g *game) update(dt float64) {
	g.elapsed += dt
	g.score += int(g.speed * dt * 10)

	// Speed up over time
	g.speed = 6.0 + g.elapsed*0.05
	if g.speed > 16.0 {
		g.speed = 16.0
	}

	g.scrollOff += g.speed * dt

	// Move obstacles toward viewer
	for i := range g.obstacles {
		if !g.obstacles[i].active {
			continue
		}
		g.obstacles[i].z -= g.speed * dt
		if g.obstacles[i].z < -1 {
			g.obstacles[i].active = false
		}
	}

	// Move coins
	for i := range g.coinPool {
		if !g.coinPool[i].active {
			continue
		}
		g.coinPool[i].z -= g.speed * dt
		if g.coinPool[i].z < -1 {
			g.coinPool[i].active = false
		}
		// Collect
		if g.coinPool[i].z < 2.0 && g.coinPool[i].z > 0 && g.coinPool[i].lane == g.runnerLane {
			g.coinPool[i].active = false
			g.coins++
			g.score += 50
		}
	}

	// Spawn obstacles
	g.spawnTimer += dt
	interval := 2.0 - g.speed*0.06
	if interval < 0.7 {
		interval = 0.7
	}
	if g.spawnTimer >= interval {
		g.spawnTimer -= interval
		g.spawnObstacle()
	}

	// Spawn coins
	g.coinTimer += dt
	if g.coinTimer >= 0.6 {
		g.coinTimer -= 0.6
		g.spawnCoin()
	}

	// Auto-dodge
	g.autoDodge()

	// Smooth lane transition
	target := float64(g.targetLane)
	diff := target - g.laneX
	if diff > 0.05 {
		g.laneX += dt * 8
		if g.laneX > target {
			g.laneX = target
		}
	} else if diff < -0.05 {
		g.laneX -= dt * 8
		if g.laneX < target {
			g.laneX = target
		}
	} else {
		g.laneX = target
		g.runnerLane = g.targetLane
	}
}

func (g *game) spawnObstacle() {
	for i := range g.obstacles {
		if !g.obstacles[i].active {
			g.obstacles[i] = obstacle{
				lane:   rand.Intn(numLanes),
				z:      float64(spawnZ),
				active: true,
			}
			return
		}
	}
}

func (g *game) spawnCoin() {
	lane := rand.Intn(numLanes)
	for j := 0; j < 3; j++ {
		for i := range g.coinPool {
			if !g.coinPool[i].active {
				g.coinPool[i] = coinObj{
					lane:   lane,
					z:      float64(spawnZ) + float64(j)*1.5,
					active: true,
				}
				break
			}
		}
	}
}

func (g *game) autoDodge() {
	danger := [numLanes]bool{}
	for i := range g.obstacles {
		if !g.obstacles[i].active {
			continue
		}
		if g.obstacles[i].z > 0 && g.obstacles[i].z < float64(dodgeLookahead) {
			danger[g.obstacles[i].lane] = true
		}
	}

	cur := g.targetLane
	if !danger[cur] {
		return
	}

	// Prefer lane with coins
	bestLane := -1
	for l := 0; l < numLanes; l++ {
		if !danger[l] {
			if bestLane == -1 {
				bestLane = l
			}
			// Check for coins in this lane
			for i := range g.coinPool {
				if g.coinPool[i].active && g.coinPool[i].lane == l && g.coinPool[i].z < float64(dodgeLookahead) {
					bestLane = l
				}
			}
		}
	}
	if bestLane >= 0 {
		g.targetLane = bestLane
	}
}

func (g *game) render() []byte {
	g.frame = g.frame[:0]

	// Move cursor home
	g.frame = append(g.frame, "\033[H"...)

	horizon := g.height / 3
	trackLeft := (g.width - trackWidth) / 2

	for row := 0; row < g.height; row++ {
		line := g.renderRow(row, horizon, trackLeft)
		g.frame = append(g.frame, line...)
		if row < g.height-1 {
			g.frame = append(g.frame, "\r\n"...)
		}
	}

	return g.frame
}

func (g *game) renderRow(row, horizon, trackLeft int) string {
	buf := make([]byte, g.width)
	for i := range buf {
		buf[i] = ' '
	}

	if row < horizon {
		// Sky
		g.drawSky(buf, row, horizon)
	} else {
		// Ground with perspective track
		g.drawGround(buf, row, horizon, trackLeft)
	}

	// HUD on first two rows
	if row == 0 {
		hud := fmt.Sprintf(" SCORE: %07d ", g.score)
		placeString(buf, g.width-len(hud)-1, hud)
	}
	if row == 1 {
		hud := fmt.Sprintf(" COINS: %d ", g.coins)
		placeString(buf, g.width-len(hud)-1, hud)
	}

	return string(buf)
}

func (g *game) drawSky(buf []byte, row, horizon int) {
	// Simple sky with stars
	if row%3 == 0 {
		pos := (row*17 + 11) % len(buf)
		if pos >= 0 && pos < len(buf) {
			buf[pos] = '.'
		}
		pos2 := (row*31 + 7) % len(buf)
		if pos2 >= 0 && pos2 < len(buf) {
			buf[pos2] = '.'
		}
	}
	// Horizon line
	if row == horizon-1 {
		for i := range buf {
			buf[i] = '_'
		}
	}
}

func (g *game) drawGround(buf []byte, row, horizon, trackLeft int) {
	// Perspective: track narrows toward horizon
	depth := float64(row-horizon) / float64(g.height-horizon)
	if depth <= 0 {
		return
	}

	// Track width scales with depth
	tw := int(float64(trackWidth) * depth)
	if tw < 3 {
		tw = 3
	}
	center := g.width / 2
	left := center - tw/2
	right := center + tw/2
	if left < 0 {
		left = 0
	}
	if right >= g.width {
		right = g.width - 1
	}

	// Ground texture outside track
	for i := range buf {
		if (i+row)%5 == 0 {
			buf[i] = '.'
		}
	}

	// Track surface
	for x := left; x <= right; x++ {
		buf[x] = ' '
	}

	// Rails (borders)
	if left >= 0 && left < g.width {
		buf[left] = '|'
	}
	if right >= 0 && right < g.width {
		buf[right] = '|'
	}

	// Lane dividers
	lw := float64(tw) / float64(numLanes)
	for l := 1; l < numLanes; l++ {
		dx := left + int(float64(l)*lw)
		if dx > left && dx < right && dx < g.width {
			// Dashed line
			scrollRow := int(g.scrollOff*2) + row
			if scrollRow%3 != 0 {
				buf[dx] = ':'
			}
		}
	}

	// Cross-ties
	scrollRow := float64(row) + g.scrollOff*3
	if int(scrollRow)%4 == 0 {
		for x := left + 1; x < right; x++ {
			if buf[x] == ' ' {
				buf[x] = '-'
			}
		}
	}

	// Draw obstacles at this row
	for i := range g.obstacles {
		obs := &g.obstacles[i]
		if !obs.active || obs.z < 0.5 {
			continue
		}
		obsDepth := 1.0 - obs.z/float64(farZ)
		if obsDepth < 0 || obsDepth > 1 {
			continue
		}
		obsRow := horizon + int(obsDepth*float64(g.height-horizon))
		if row >= obsRow-2 && row <= obsRow {
			obsTw := int(float64(trackWidth) * (1.0 - obs.z/float64(farZ)))
			if obsTw < 3 {
				continue
			}
			obsLeft := center - obsTw/2
			obsLW := float64(obsTw) / float64(numLanes)
			ox := obsLeft + int(float64(obs.lane)*obsLW+obsLW*0.15)
			ow := int(obsLW * 0.7)
			if ow < 1 {
				ow = 1
			}
			for x := ox; x < ox+ow && x < g.width; x++ {
				if x >= 0 {
					if row == obsRow-2 {
						buf[x] = '#'
					} else {
						buf[x] = '#'
					}
				}
			}
		}
	}

	// Draw coins at this row
	for i := range g.coinPool {
		cn := &g.coinPool[i]
		if !cn.active || cn.z < 0.5 {
			continue
		}
		coinDepth := 1.0 - cn.z/float64(farZ)
		if coinDepth < 0 || coinDepth > 1 {
			continue
		}
		coinRow := horizon + int(coinDepth*float64(g.height-horizon))
		if row == coinRow {
			cnTw := int(float64(trackWidth) * (1.0 - cn.z/float64(farZ)))
			if cnTw < 3 {
				continue
			}
			cnLeft := center - cnTw/2
			cnLW := float64(cnTw) / float64(numLanes)
			cx := cnLeft + int(float64(cn.lane)*cnLW+cnLW*0.5)
			if cx >= 0 && cx < g.width {
				buf[cx] = 'o'
			}
		}
	}

	// Draw runner
	runnerDepth := 0.85 // near bottom
	runnerScreenRow := horizon + int(runnerDepth*float64(g.height-horizon))
	rTw := int(float64(trackWidth) * runnerDepth)
	rLeft := center - rTw/2
	rLW := float64(rTw) / float64(numLanes)
	rx := rLeft + int(g.laneX*rLW+rLW*0.5)

	// Runner is 3 rows tall
	if row == runnerScreenRow-2 {
		// Head
		if rx >= 0 && rx < g.width {
			buf[rx] = 'O'
		}
	} else if row == runnerScreenRow-1 {
		// Body
		placeStringBytes(buf, rx-1, []byte("/|\\"))
	} else if row == runnerScreenRow {
		// Legs - walking animation
		frame := int(g.elapsed*8) % 4
		legs := [4]string{"/ \\", "| |", "\\ /", "| |"}
		placeStringBytes(buf, rx-1, []byte(legs[frame]))
	}

	return
}

func placeString(buf []byte, x int, s string) {
	placeStringBytes(buf, x, []byte(s))
}

func placeStringBytes(buf []byte, x int, s []byte) {
	for i, c := range s {
		px := x + i
		if px >= 0 && px < len(buf) {
			buf[px] = c
		}
	}
}

func main() {
	fd := int(os.Stdin.Fd())
	oldState, err := term.MakeRaw(fd)
	if err != nil {
		fmt.Fprintf(os.Stderr, "failed to set raw mode: %v\n", err)
		os.Exit(1)
	}
	defer term.Restore(fd, oldState)

	quit := make(chan struct{})
	var once sync.Once
	doQuit := func() { once.Do(func() { close(quit) }) }

	sigs := make(chan os.Signal, 1)
	signal.Notify(sigs, syscall.SIGINT, syscall.SIGTERM)
	go func() { <-sigs; doQuit() }()
	go func() {
		b := make([]byte, 1)
		for {
			n, err := os.Stdin.Read(b)
			if err != nil || n == 0 {
				return
			}
			if b[0] == 'q' || b[0] == 3 {
				doQuit()
				return
			}
		}
	}()

	w, h, err := term.GetSize(int(os.Stdout.Fd()))
	if err != nil {
		w, h = 80, 24
	}

	g := newGame(w, h)
	g.frame = make([]byte, 0, w*h*2)

	// Setup screen
	os.Stdout.WriteString("\033[?1049h") // alt screen
	os.Stdout.WriteString("\033[?25l")   // hide cursor
	os.Stdout.WriteString("\033[2J")     // clear
	defer func() {
		os.Stdout.WriteString("\033[?25h")   // show cursor
		os.Stdout.WriteString("\033[?1049l") // restore screen
	}()

	// Title
	title := "SUBWAY SURFER - press q to quit"
	os.Stdout.WriteString(fmt.Sprintf("\033[1;%dH%s", (w-len(title))/2, title))
	time.Sleep(time.Second)

	ticker := time.NewTicker(time.Second / targetFPS)
	defer ticker.Stop()
	last := time.Now()

	for {
		select {
		case <-quit:
			return
		case <-ticker.C:
			now := time.Now()
			dt := now.Sub(last).Seconds()
			if dt > 0.1 {
				dt = 0.1
			}
			last = now

			// Check resize
			if nw, nh, err := term.GetSize(int(os.Stdout.Fd())); err == nil {
				if nw != g.width || nh != g.height {
					g.width = nw
					g.height = nh
					os.Stdout.WriteString("\033[2J")
				}
			}

			g.update(dt)
			frame := g.render()
			os.Stdout.Write(frame)
		}
	}
}
