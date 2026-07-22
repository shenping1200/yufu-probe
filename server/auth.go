package main

import (
	"crypto/rand"
	"database/sql"
	"encoding/hex"
	"net/http"
	"time"
)

const sessionCookie = "probe_session"

// createSession 生成并保存一个登录会话，返回 token
func createSession(db *sql.DB) (string, error) {
	b := make([]byte, 32)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	token := hex.EncodeToString(b)
	_, err := db.Exec(`INSERT INTO sessions (token, created_at) VALUES (?, ?)`, token, time.Now().Unix())
	return token, err
}

func validSession(db *sql.DB, token string) bool {
	if token == "" {
		return false
	}
	var n int
	err := db.QueryRow(`SELECT count(*) FROM sessions WHERE token=?`, token).Scan(&n)
	return err == nil && n > 0
}

func deleteSession(db *sql.DB, token string) {
	db.Exec(`DELETE FROM sessions WHERE token=?`, token)
}

// requireLogin 校验 session cookie 的中间件
func requireLogin(db *sql.DB, next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		c, err := r.Cookie(sessionCookie)
		if err != nil || !validSession(db, c.Value) {
			http.Error(w, "unauthorized", http.StatusUnauthorized)
			return
		}
		next(w, r)
	}
}
