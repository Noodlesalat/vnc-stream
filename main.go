package main

import (
	"bytes"
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"html/template"
	"image"
	"image/color"
	"image/jpeg"
	"log"
	"net"
	"net/http"
	"os"
	"sync"
	"time"

	vnc "github.com/amitbet/vnc2video"
)

// State management variables protected by stateMu
var (
	stateMu         sync.RWMutex
	vncConnected    bool
	vncConnErr      error
	activeCanvas    *vnc.VncCanvas
	vncCanvasWidth  int
	vncCanvasHeight int
	lastFrameBytes  []byte
)

// ClientBroker broadcasts MJPEG frames to all active HTTP subscribers
type ClientBroker struct {
	mu      sync.Mutex
	clients map[chan []byte]bool
}

func NewClientBroker() *ClientBroker {
	return &ClientBroker{
		clients: make(map[chan []byte]bool),
	}
}

func (b *ClientBroker) Register(ch chan []byte) {
	b.mu.Lock()
	b.clients[ch] = true
	b.mu.Unlock()
}

func (b *ClientBroker) Unregister(ch chan []byte) {
	b.mu.Lock()
	delete(b.clients, ch)
	b.mu.Unlock()
}

func (b *ClientBroker) Broadcast(jpegBytes []byte) {
	b.mu.Lock()
	defer b.mu.Unlock()
	for ch := range b.clients {
		select {
		case ch <- jpegBytes:
		default:
			// Drop frame if client is too slow to avoid blocking other clients
		}
	}
}

// Global cached test pattern bytes
var (
	testPatternBytes []byte
	testPatternOnce  sync.Once
)

// generateTestPattern creates a classic television test card (SMPTE-style color bars)
func generateTestPattern(width, height int) image.Image {
	img := image.NewRGBA(image.Rect(0, 0, width, height))

	// 7 vertical color bars
	colors := []color.RGBA{
		{192, 192, 192, 255}, // Light grey
		{224, 224, 0, 255},   // Yellow
		{0, 224, 224, 255},   // Cyan
		{0, 224, 0, 255},     // Green
		{224, 0, 224, 255},   // Magenta
		{224, 0, 0, 255},     // Red
		{0, 0, 224, 255},     // Blue
	}

	barWidth := width / len(colors)
	for i, col := range colors {
		xStart := i * barWidth
		xEnd := (i + 1) * barWidth
		if i == len(colors)-1 {
			xEnd = width
		}
		for x := xStart; x < xEnd; x++ {
			for y := 0; y < height; y++ {
				img.Set(x, y, col)
			}
		}
	}

	// Dark bar at the bottom
	bottomStart := height * 3 / 4
	for x := 0; x < width; x++ {
		for y := bottomStart; y < height; y++ {
			img.Set(x, y, color.RGBA{20, 20, 20, 255})
		}
	}

	// Red card indicating offline status
	blockW := 220
	blockH := 40
	blockX := (width - blockW) / 2
	blockY := bottomStart + (height-bottomStart-blockH)/2
	for x := blockX; x < blockX+blockW; x++ {
		for y := blockY; y < blockY+blockH; y++ {
			img.Set(x, y, color.RGBA{220, 20, 60, 255})
		}
	}

	return img
}

func getTestPattern(quality int) []byte {
	testPatternOnce.Do(func() {
		img := generateTestPattern(800, 600)
		var buf bytes.Buffer
		if err := jpeg.Encode(&buf, img, &jpeg.Options{Quality: quality}); err != nil {
			log.Printf("Error encoding test pattern: %v", err)
			testPatternBytes = []byte{}
			return
		}
		testPatternBytes = buf.Bytes()
	})
	return testPatternBytes
}

func setVncState(conn *vnc.ClientConn, err error) {
	stateMu.Lock()
	defer stateMu.Unlock()
	if conn != nil {
		vncConnected = true
		vncConnErr = nil
		activeCanvas = conn.Canvas
		vncCanvasWidth = int(conn.Width())
		vncCanvasHeight = int(conn.Height())
	} else {
		vncConnected = false
		vncConnErr = err
		activeCanvas = nil
		vncCanvasWidth = 0
		vncCanvasHeight = 0
	}
}

// startVNCManager runs the background connection and auto-reconnection loop
func startVNCManager(host string, port int, password string, bpp int) {
	address := fmt.Sprintf("%s:%d", host, port)

	for {
		log.Printf("[VNC] Connecting to %s...", address)
		dialer, err := net.DialTimeout("tcp", address, 5*time.Second)
		if err != nil {
			log.Printf("[VNC] Connection failed: %v. Retrying in 5 seconds...", err)
			setVncState(nil, err)
			time.Sleep(5 * time.Second)
			continue
		}

		var secHandlers []vnc.SecurityHandler
		if password == "" {
			secHandlers = []vnc.SecurityHandler{
				&vnc.ClientAuthNone{},
			}
		} else {
			secHandlers = []vnc.SecurityHandler{
				&vnc.ClientAuthVNC{Password: []byte(password)},
			}
		}

		cchServer := make(chan vnc.ServerMessage, 100)
		cchClient := make(chan vnc.ClientMessage, 100)
		errorCh := make(chan error, 10)

		var pf vnc.PixelFormat
		switch bpp {
		case 8:
			pf = vnc.PixelFormat8bit
		case 16:
			pf = vnc.PixelFormat16bit
		default:
			pf = vnc.PixelFormat32bit
		}

		ccflags := &vnc.ClientConfig{
			SecurityHandlers: secHandlers,
			DrawCursor:       true,
			PixelFormat:      pf,
			ClientMessageCh:  cchClient,
			ServerMessageCh:  cchServer,
			Messages:         vnc.DefaultServerMessages,
			Encodings: []vnc.Encoding{
				&vnc.RawEncoding{},
				&vnc.TightEncoding{},
				&vnc.HextileEncoding{},
				&vnc.ZRLEEncoding{},
				&vnc.CopyRectEncoding{},
				&vnc.CursorPseudoEncoding{},
				&vnc.CursorPosPseudoEncoding{},
				&vnc.ZLibEncoding{},
				&vnc.RREEncoding{},
			},
			ErrorCh: errorCh,
		}

		vncConnection, err := vnc.Connect(context.Background(), dialer, ccflags)
		if err != nil {
			dialer.Close()
			log.Printf("[VNC] Handshake/Auth failed: %v. Retrying in 5 seconds...", err)
			setVncState(nil, err)
			time.Sleep(5 * time.Second)
			continue
		}

		log.Printf("[VNC] Connected successfully. Resolution: %dx%d", vncConnection.Width(), vncConnection.Height())
		setVncState(vncConnection, nil)

		for _, enc := range ccflags.Encodings {
			myRenderer, ok := enc.(vnc.Renderer)
			if ok {
				myRenderer.SetTargetImage(vncConnection.Canvas)
			}
		}

		// Request supported encodings
		_ = vncConnection.SetEncodings([]vnc.EncodingType{
			vnc.EncCursorPseudo,
			vnc.EncPointerPosPseudo,
			vnc.EncCopyRect,
			vnc.EncTight,
			vnc.EncZRLE,
			vnc.EncHextile,
			vnc.EncZlib,
			vnc.EncRRE,
		})

		// Frame request loop: request a new frame buffer update whenever one completes
		ctx, cancel := context.WithCancel(context.Background())
		go func() {
			for {
				select {
				case <-ctx.Done():
					return
				case msg, ok := <-cchServer:
					if !ok {
						return
					}
					if msg.Type() == vnc.FramebufferUpdateMsgType {
						reqMsg := vnc.FramebufferUpdateRequest{
							Inc:    1,
							X:      0,
							Y:      0,
							Width:  vncConnection.Width(),
							Height: vncConnection.Height(),
						}
						_ = reqMsg.Write(vncConnection)
					}
				}
			}
		}()

		// Monitor connection health
		err = <-errorCh
		log.Printf("[VNC] Connection closed: %v. Reconnecting in 2 seconds...", err)
		cancel()
		vncConnection.Close()
		dialer.Close()
		setVncState(nil, err)

		time.Sleep(2 * time.Second)
	}
}

// startEncodingLoop periodically captures and compresses VNC screen/test pattern frames to JPEG
func startEncodingLoop(ctx context.Context, broker *ClientBroker, fps int, quality int) {
	ticker := time.NewTicker(time.Second / time.Duration(fps))
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			var imgBytes []byte

			stateMu.RLock()
			isConnected := vncConnected
			canvas := activeCanvas
			stateMu.RUnlock()

			if isConnected && canvas != nil && canvas.Image != nil {
				var buf bytes.Buffer
				// Encode canvas (including dynamic pointer coordinates and updates) to JPEG
				err := jpeg.Encode(&buf, canvas, &jpeg.Options{Quality: quality})
				if err == nil {
					imgBytes = buf.Bytes()
					stateMu.Lock()
					lastFrameBytes = imgBytes
					stateMu.Unlock()
				} else {
					log.Printf("[Encoder] Error encoding canvas to JPEG: %v", err)
				}
			}

			if imgBytes == nil {
				// VNC is offline, stream test pattern instead
				imgBytes = getTestPattern(quality)
			}

			broker.Broadcast(imgBytes)
		}
	}
}

func handleStream(broker *ClientBroker, quality int) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		ch := make(chan []byte, 10)
		broker.Register(ch)
		defer broker.Unregister(ch)

		w.Header().Set("Content-Type", "multipart/x-mixed-replace; boundary=frame")
		w.Header().Set("Cache-Control", "no-cache, private, max-age=0, no-transform, no-store, must-revalidate")
		w.Header().Set("Pragma", "no-cache")
		w.Header().Set("Expires", "0")
		w.Header().Set("Connection", "close")

		// Send the initial frame immediately so client has a starting display
		stateMu.RLock()
		lastFrame := lastFrameBytes
		isConnected := vncConnected
		stateMu.RUnlock()

		if isConnected && lastFrame != nil {
			select {
			case ch <- lastFrame:
			default:
			}
		} else {
			select {
			case ch <- getTestPattern(quality):
			default:
			}
		}

		ctx := r.Context()
		for {
			select {
			case <-ctx.Done():
				return
			case jpegBytes, ok := <-ch:
				if !ok {
					return
				}
				_, err := fmt.Fprintf(w, "--frame\r\nContent-Type: image/jpeg\r\nContent-Length: %d\r\n\r\n", len(jpegBytes))
				if err != nil {
					return
				}
				_, err = w.Write(jpegBytes)
				if err != nil {
					return
				}
				_, err = w.Write([]byte("\r\n"))
				if err != nil {
					return
				}
				if flusher, ok := w.(http.Flusher); ok {
					flusher.Flush()
				}
			}
		}
	}
}

type DashboardData struct {
	VNCHost   string
	VNCPort   int
	HTTPPort  string
	FPS       int
	Width     int
	Height    int
	Connected bool
	ErrorMsg  string
	BPP       int
}

func handleDashboard(vncHost string, vncPort int, httpListen string, fps int, bpp int) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/" {
			http.NotFound(w, r)
			return
		}

		stateMu.RLock()
		connected := vncConnected
		var errStr string
		if vncConnErr != nil {
			errStr = vncConnErr.Error()
		}
		wVal := vncCanvasWidth
		hVal := vncCanvasHeight
		stateMu.RUnlock()

		data := DashboardData{
			VNCHost:   vncHost,
			VNCPort:   vncPort,
			HTTPPort:  httpListen,
			FPS:       fps,
			Width:     wVal,
			Height:    hVal,
			Connected: connected,
			ErrorMsg:  errStr,
			BPP:       bpp,
		}

		tmpl, err := template.New("dashboard").Parse(dashboardHTML)
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}

		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		if err := tmpl.Execute(w, data); err != nil {
			log.Printf("Template execute error: %v", err)
		}
	}
}

type Config struct {
	Host     string `json:"host"`
	Port     int    `json:"port"`
	Password string `json:"password"`
	Listen   string `json:"listen"`
	FPS      int    `json:"fps"`
	Quality  int    `json:"quality"`
	BPP      int    `json:"bpp"`
}

func loadConfig() (*Config, error) {
	// Initialize with default values
	cfg := &Config{
		Host:     "localhost",
		Port:     5900,
		Password: "",
		Listen:   ":8080",
		FPS:      10,
		Quality:  80,
		BPP:      32,
	}

	// Register command-line flags
	hostFlag := flag.String("host", "localhost", "VNC server hostname/IP")
	portFlag := flag.Int("port", 5900, "VNC server port")
	passwordFlag := flag.String("password", "", "VNC server password")
	listenFlag := flag.String("listen", ":8080", "HTTP server address to listen on")
	fpsFlag := flag.Int("fps", 10, "Target frame rate (Frames Per Second)")
	qualityFlag := flag.Int("quality", 80, "JPEG compression quality (1-100)")
	bppFlag := flag.Int("bpp", 32, "VNC color depth in bits per pixel (8, 16, or 32)")
	configPath := flag.String("config", "", "Path to JSON configuration file")

	flag.Parse()

	// If configuration file is specified, load and parse it
	if *configPath != "" {
		data, err := os.ReadFile(*configPath)
		if err != nil {
			return nil, fmt.Errorf("failed to read config file: %w", err)
		}
		if err := json.Unmarshal(data, cfg); err != nil {
			return nil, fmt.Errorf("failed to parse config JSON: %w", err)
		}
	}

	// Track which flags were explicitly set by the user to override settings
	visited := make(map[string]bool)
	flag.Visit(func(f *flag.Flag) {
		visited[f.Name] = true
	})

	if visited["host"] {
		cfg.Host = *hostFlag
	}
	if visited["port"] {
		cfg.Port = *portFlag
	}
	if visited["password"] {
		cfg.Password = *passwordFlag
	}
	if visited["listen"] {
		cfg.Listen = *listenFlag
	}
	if visited["fps"] {
		cfg.FPS = *fpsFlag
	}
	if visited["quality"] {
		cfg.Quality = *qualityFlag
	}
	if visited["bpp"] {
		cfg.BPP = *bppFlag
	}

	// Sanitize and boundary check settings
	if cfg.FPS <= 0 {
		cfg.FPS = 10
	}
	if cfg.Quality < 1 || cfg.Quality > 100 {
		cfg.Quality = 80
	}
	if cfg.BPP != 8 && cfg.BPP != 16 && cfg.BPP != 32 {
		cfg.BPP = 32
	}

	return cfg, nil
}

func main() {
	cfg, err := loadConfig()
	if err != nil {
		log.Fatalf("Configuration error: %v", err)
	}

	broker := NewClientBroker()

	// Launch VNC manager and periodic frame encoding goroutines
	go startVNCManager(cfg.Host, cfg.Port, cfg.Password, cfg.BPP)
	go startEncodingLoop(context.Background(), broker, cfg.FPS, cfg.Quality)

	http.HandleFunc("/", handleDashboard(cfg.Host, cfg.Port, cfg.Listen, cfg.FPS, cfg.BPP))
	http.HandleFunc("/stream", handleStream(broker, cfg.Quality))

	log.Printf("[HTTP] Stream server starting on http://%s/", cfg.Listen)
	if err := http.ListenAndServe(cfg.Listen, nil); err != nil {
		log.Fatalf("[HTTP] Server failed: %v", err)
	}
}

// Dashboard HTML and CSS (Premium Dark Mode UI)
const dashboardHTML = `<!DOCTYPE html>
<html lang="de">
<head>
    <meta charset="UTF-8">
    <meta name="viewport" content="width=device-width, initial-scale=1.0">
    <title>VNC MJPEG Streamer</title>
    <link href="https://fonts.googleapis.com/css2?family=Inter:wght@300;400;500;600;700&display=swap" rel="stylesheet">
    <style>
        :root {
            --bg-color: #0d0e12;
            --card-bg: #161822;
            --accent-color: #6366f1;
            --accent-glow: rgba(99, 102, 241, 0.15);
            --text-main: #f3f4f6;
            --text-muted: #9ca3af;
            --success-color: #10b981;
            --danger-color: #ef4444;
            --border-color: #2e303e;
        }

        * {
            box-sizing: border-box;
            margin: 0;
            padding: 0;
        }

        body {
            font-family: 'Inter', -apple-system, sans-serif;
            background-color: var(--bg-color);
            color: var(--text-main);
            min-height: 100vh;
            display: flex;
            flex-direction: column;
            align-items: center;
            justify-content: flex-start;
            padding: 2rem;
        }

        .wrapper {
            width: 100%;
            max-width: 1200px;
        }

        header {
            width: 100%;
            display: flex;
            justify-content: space-between;
            align-items: center;
            padding-bottom: 1.5rem;
            border-bottom: 1px solid var(--border-color);
            margin-bottom: 2rem;
        }

        .logo-section h1 {
            font-size: 1.5rem;
            font-weight: 700;
            background: linear-gradient(135deg, #a5b4fc, var(--accent-color));
            -webkit-background-clip: text;
            -webkit-text-fill-color: transparent;
        }

        .logo-section p {
            font-size: 0.875rem;
            color: var(--text-muted);
            margin-top: 0.25rem;
        }

        .status-badge {
            display: flex;
            align-items: center;
            gap: 0.5rem;
            padding: 0.5rem 1rem;
            border-radius: 9999px;
            font-size: 0.875rem;
            font-weight: 600;
        }

        .status-badge.status-online {
            background: rgba(16, 185, 129, 0.1);
            color: var(--success-color);
            border: 1px solid rgba(16, 185, 129, 0.2);
        }

        .status-badge.status-offline {
            background: rgba(239, 68, 68, 0.1);
            color: var(--danger-color);
            border: 1px solid rgba(239, 68, 68, 0.2);
        }

        .status-dot {
            width: 8px;
            height: 8px;
            border-radius: 50%;
        }

        .status-dot.dot-online {
            background-color: var(--success-color);
            box-shadow: 0 0 10px var(--success-color);
            animation: pulse 2s infinite;
        }

        .status-dot.dot-offline {
            background-color: var(--danger-color);
            box-shadow: 0 0 10px var(--danger-color);
        }

        @keyframes pulse {
            0% { transform: scale(0.95); box-shadow: 0 0 0 0 rgba(16, 185, 129, 0.7); }
            70% { transform: scale(1); box-shadow: 0 0 0 6px rgba(16, 185, 129, 0); }
            100% { transform: scale(0.95); box-shadow: 0 0 0 0 rgba(16, 185, 129, 0); }
        }

        .container {
            display: grid;
            grid-template-columns: 3fr 1fr;
            gap: 2rem;
        }

        @media (max-width: 900px) {
            .container {
                grid-template-columns: 1fr;
            }
        }

        .stream-card {
            background-color: var(--card-bg);
            border: 1px solid var(--border-color);
            border-radius: 16px;
            padding: 1.5rem;
            display: flex;
            flex-direction: column;
            box-shadow: 0 10px 30px rgba(0, 0, 0, 0.3);
        }

        .stream-wrapper {
            width: 100%;
            border-radius: 8px;
            overflow: hidden;
            background-color: #000;
            display: flex;
            justify-content: center;
            align-items: center;
            aspect-ratio: 16/9;
            border: 1px solid var(--border-color);
        }

        .stream-image {
            max-width: 100%;
            max-height: 100%;
            object-fit: contain;
        }

        .sidebar {
            display: flex;
            flex-direction: column;
            gap: 1.5rem;
        }

        .info-card {
            background-color: var(--card-bg);
            border: 1px solid var(--border-color);
            border-radius: 16px;
            padding: 1.5rem;
            box-shadow: 0 10px 30px rgba(0, 0, 0, 0.3);
        }

        .info-card h3 {
            font-size: 1rem;
            font-weight: 600;
            margin-bottom: 1.25rem;
            color: var(--text-main);
            border-bottom: 1px solid var(--border-color);
            padding-bottom: 0.5rem;
        }

        .info-row {
            display: flex;
            justify-content: space-between;
            margin-bottom: 0.875rem;
            font-size: 0.875rem;
        }

        .info-row:last-child {
            margin-bottom: 0;
        }

        .info-label {
            color: var(--text-muted);
        }

        .info-value {
            font-weight: 500;
            font-family: monospace;
        }

        .error-banner {
            background-color: rgba(239, 68, 68, 0.08);
            border: 1px solid rgba(239, 68, 68, 0.2);
            border-radius: 12px;
            padding: 1rem;
            margin-bottom: 1.5rem;
            width: 100%;
        }

        .error-banner h3 {
            color: var(--danger-color);
            font-size: 0.95rem;
            font-weight: 600;
            margin-bottom: 0.25rem;
        }

        .error-banner p {
            color: var(--text-muted);
            font-size: 0.85rem;
            font-family: monospace;
            word-break: break-all;
        }
    </style>
    <script>
        // Check VNC connection status periodically to update UI details
        setInterval(() => {
            fetch('/')
                .then(response => response.text())
                .then(html => {
                    const parser = new DOMParser();
                    const doc = parser.parseFromString(html, 'text/html');
                    
                    // Update status badge
                    const currentBadge = document.querySelector('.status-badge');
                    const newBadge = doc.querySelector('.status-badge');
                    if (currentBadge && newBadge) {
                        currentBadge.className = newBadge.className;
                        currentBadge.innerHTML = newBadge.innerHTML;
                    }

                    // Update connection statistics
                    const currentRows = document.querySelectorAll('.info-row');
                    const newRows = doc.querySelectorAll('.info-row');
                    currentRows.forEach((row, index) => {
                        if (newRows[index]) {
                            row.innerHTML = newRows[index].innerHTML;
                        }
                    });

                    // Update error banner if any
                    const currentBanner = document.querySelector('.error-banner');
                    const newBanner = doc.querySelector('.error-banner');
                    const streamCard = document.querySelector('.stream-card');
                    
                    if (newBanner) {
                        if (currentBanner) {
                            currentBanner.innerHTML = newBanner.innerHTML;
                        } else {
                            const tempDiv = document.createElement('div');
                            tempDiv.className = 'error-banner';
                            tempDiv.innerHTML = newBanner.innerHTML;
                            streamCard.insertBefore(tempDiv, streamCard.firstChild);
                        }
                    } else if (currentBanner) {
                        currentBanner.remove();
                    }
                })
                .catch(err => console.error("Error updating stats:", err));
        }, 3000);
    </script>
</head>
<body>
    <div class="wrapper">
        <header>
            <div class="logo-section">
                <h1>VNC MJPEG Streamer</h1>
                <p>Live-Bildschirminhalt über HTTP</p>
            </div>
            {{if .Connected}}
            <div class="status-badge status-online">
                <span class="status-dot dot-online"></span>
                <span>LIVE</span>
            </div>
            {{else}}
            <div class="status-badge status-offline">
                <span class="status-dot dot-offline"></span>
                <span>OFFLINE</span>
            </div>
            {{end}}
        </header>

        <div class="container">
            <div class="stream-card">
                {{if not .Connected}}
                <div class="error-banner">
                    <h3>Verbindung fehlgeschlagen</h3>
                    <p>{{if .ErrorMsg}}{{.ErrorMsg}}{{else}}Verbindung zum VNC-Server wird aufgebaut...{{end}}</p>
                </div>
                {{end}}
                <div class="stream-wrapper">
                    <img class="stream-image" src="/stream" alt="VNC Stream">
                </div>
            </div>

            <div class="sidebar">
                <div class="info-card">
                    <h3>Verbindungsdaten</h3>
                    <div class="info-row">
                        <span class="info-label">VNC Server</span>
                        <span class="info-value">{{.VNCHost}}:{{.VNCPort}}</span>
                    </div>
                    <div class="info-row">
                        <span class="info-label">Auflösung</span>
                        <span class="info-value">{{if .Connected}}{{.Width}}x{{.Height}}{{else}}Offline{{end}}</span>
                    </div>
                    <div class="info-row">
                        <span class="info-label">Ziel-FPS</span>
                        <span class="info-value">{{.FPS}}</span>
                    </div>
                    <div class="info-row">
                        <span class="info-label">Farbtiefe (BPP)</span>
                        <span class="info-value">{{.BPP}} bit</span>
                    </div>
                    <div class="info-row">
                        <span class="info-label">Listen Address</span>
                        <span class="info-value">{{.HTTPPort}}</span>
                    </div>
                </div>
            </div>
        </div>
    </div>
</body>
</html>
`
