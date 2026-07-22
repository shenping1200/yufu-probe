package main

import (
	"database/sql"
	"embed"
	"encoding/json"
	"io/fs"
	"net/http"
	"time"

	"github.com/gorilla/mux"
	"github.com/gorilla/websocket"
)

//go:embed static
var staticFS embed.FS

var staticSubFS fs.FS

func init() {
	sub, err := fs.Sub(staticFS, "static")
	if err == nil {
		staticSubFS = sub
	}
}

var upgrader = websocket.Upgrader{
	CheckOrigin: func(r *http.Request) bool { return true },
}

// AgentReport 客户端上报的数据结构
type AgentReport struct {
	UUID      string  `json:"uuid"`
	Hostname  string  `json:"hostname"`
	IP        string  `json:"ip"`
	PublicIP  string  `json:"public_ip"`
	OS        string  `json:"os"`
	Platform  string  `json:"platform"`
	BootTime  int64   `json:"boot_time"`
	Uptime    int64   `json:"uptime"`
	CPU       float64 `json:"cpu"`
	CPUCount  int     `json:"cpu_count"`
	MemUsed   float64 `json:"mem_used"`
	MemTotal  float64 `json:"mem_total"`
	DiskUsed  float64 `json:"disk_used"`
	DiskTotal float64 `json:"disk_total"`
	RxRate    float64 `json:"rx_rate"`
	TxRate    float64 `json:"tx_rate"`
	RxDelta   float64 `json:"rx_delta"`
	TxDelta   float64 `json:"tx_delta"`
}

// broadcastAgents 查询最新机器列表并推送给所有 viewer
func broadcastAgents(hub *Hub, db *sql.DB) {
	list, err := ListAgents(db, time.Now().Format("2006-01"))
	if err != nil {
		return
	}
	payload, err := json.Marshal(map[string]interface{}{"type": "agents", "data": list})
	if err != nil {
		return
	}
	hub.BroadcastToViewers(payload)
}

func loginHandler(cfg *Config, db *sql.DB) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		var req struct {
			Username string `json:"username"`
			Password string `json:"password"`
		}
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			http.Error(w, "bad request", http.StatusBadRequest)
			return
		}
		if req.Username != cfg.Admin.Username || req.Password != cfg.Admin.Password {
			http.Error(w, "invalid credentials", http.StatusUnauthorized)
			return
		}
		token, err := createSession(db)
		if err != nil {
			http.Error(w, "internal error", http.StatusInternalServerError)
			return
		}
		http.SetCookie(w, &http.Cookie{
			Name:     sessionCookie,
			Value:    token,
			Path:     "/",
			HttpOnly: true,
			SameSite: http.SameSiteLaxMode,
		})
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]bool{"ok": true})
	}
}

func logoutHandler(db *sql.DB) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if c, err := r.Cookie(sessionCookie); err == nil {
			deleteSession(db, c.Value)
		}
		http.SetCookie(w, &http.Cookie{
			Name:     sessionCookie,
			Value:    "",
			Path:     "/",
			HttpOnly: true,
			MaxAge:   -1,
		})
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]bool{"ok": true})
	}
}

func meHandler(cfg *Config) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]string{"username": cfg.Admin.Username})
	}
}

func agentsHandler(db *sql.DB) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		list, err := ListAgents(db, time.Now().Format("2006-01"))
		if err != nil {
			http.Error(w, "internal error", http.StatusInternalServerError)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(list)
	}
}

func aliasHandler(db *sql.DB) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		uuid := mux.Vars(r)["uuid"]
		var req struct {
			Alias string `json:"alias"`
		}
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			http.Error(w, "bad request", http.StatusBadRequest)
			return
		}
		if err := SetAlias(db, uuid, req.Alias); err != nil {
			http.Error(w, "internal error", http.StatusInternalServerError)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]bool{"ok": true})
	}
}

func trafficHandler(db *sql.DB) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		uuid := mux.Vars(r)["uuid"]
		list, err := GetTrafficHistory(db, uuid)
		if err != nil {
			http.Error(w, "internal error", http.StatusInternalServerError)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(list)
	}
}

// agentWSHandler 接收客户端上报（需 Token）
func agentWSHandler(cfg *Config, db *sql.DB, hub *Hub) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Query().Get("token") != cfg.AgentToken {
			http.Error(w, "forbidden", http.StatusForbidden)
			return
		}
		conn, err := upgrader.Upgrade(w, r, nil)
		if err != nil {
			return
		}
		defer conn.Close()
		for {
			_, data, err := conn.ReadMessage()
			if err != nil {
				return
			}
			var rep AgentReport
			if err := json.Unmarshal(data, &rep); err != nil || rep.UUID == "" {
				continue
			}
			// 优先用公网 IP 做地理定位，缺失时回退到上报的内网 IP
			geoIP := rep.PublicIP
			if geoIP == "" {
				geoIP = rep.IP
			}
		row := AgentRow{
			UUID: rep.UUID, Hostname: rep.Hostname, IP: rep.IP,
			OS: rep.OS, Platform: rep.Platform,
			BootTime: rep.BootTime, Uptime: rep.Uptime, CPU: rep.CPU, CPUCount: rep.CPUCount,
			MemUsed: rep.MemUsed, MemTotal: rep.MemTotal,
			DiskUsed: rep.DiskUsed, DiskTotal: rep.DiskTotal,
			RxRate: rep.RxRate, TxRate: rep.TxRate,
			Country: lookupCountry(db, geoIP, rep.UUID),
		}
			if err := UpsertAgent(db, row); err != nil {
				continue
			}
			if rep.RxDelta > 0 || rep.TxDelta > 0 {
				AddTraffic(db, rep.UUID, time.Now().Format("2006-01"), rep.RxDelta, rep.TxDelta)
			}
			broadcastAgents(hub, db)
		}
	}
}

// viewerWSHandler 浏览器实时订阅（需登录 session）
func viewerWSHandler(db *sql.DB, hub *Hub) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		c, err := r.Cookie(sessionCookie)
		if err != nil || !validSession(db, c.Value) {
			http.Error(w, "unauthorized", http.StatusUnauthorized)
			return
		}
		conn, err := upgrader.Upgrade(w, r, nil)
		if err != nil {
			return
		}
		client := &Client{hub: hub, conn: conn, send: make(chan []byte, 16), role: "viewer"}
		hub.addViewer(client)
		go client.writePump()
		defer func() {
			hub.removeViewer(client)
			conn.Close()
		}()
		broadcastAgents(hub, db)
		for {
			if _, _, err := conn.ReadMessage(); err != nil {
				return
			}
		}
	}
}

func setupRoutes(cfg *Config, db *sql.DB, hub *Hub) http.Handler {
	r := mux.NewRouter()
	r.HandleFunc("/api/login", loginHandler(cfg, db)).Methods("POST")
	r.HandleFunc("/api/logout", logoutHandler(db)).Methods("POST")
	r.HandleFunc("/api/me", requireLogin(db, meHandler(cfg))).Methods("GET")
	r.HandleFunc("/api/agents", requireLogin(db, agentsHandler(db))).Methods("GET")
	r.HandleFunc("/api/agents/{uuid}/alias", requireLogin(db, aliasHandler(db))).Methods("PUT")
	r.HandleFunc("/api/agents/{uuid}/traffic", requireLogin(db, trafficHandler(db))).Methods("GET")
	r.HandleFunc("/ws/agent", agentWSHandler(cfg, db, hub))
	r.HandleFunc("/ws/viewer", viewerWSHandler(db, hub))
	r.PathPrefix("/").Handler(http.FileServer(http.FS(staticSubFS)))
	return r
}
