package main

import (
	"crypto/rand"
	"database/sql"
	"encoding/hex"
	"net/http"
	"time"
)

const sessionCookie = "probe_session"

const (
	roleAdmin   = "admin"
	roleVisitor = "visitor"
)

// createSession 生成并保存一个登录会话，role: admin/visitor
func createSession(db *sql.DB, role string) (string, error) {
	b := make([]byte, 32)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	token := hex.EncodeToString(b)
	_, err := db.Exec(`INSERT INTO sessions (token, created_at, role) VALUES (?, ?, ?)`, token, time.Now().Unix(), role)
	return token, err
}

// sessionRole 读取会话角色。空/不存在返回空串。
func sessionRole(db *sql.DB, token string) string {
	var role string
	if err := db.QueryRow(`SELECT role FROM sessions WHERE token=?`, token).Scan(&role); err != nil {
		return ""
	}
	return role
}

func validSession(db *sql.DB, token string) bool {
	return sessionRole(db, token) != ""
}

func deleteSession(db *sql.DB, token string) {
	db.Exec(`DELETE FROM sessions WHERE token=?`, token)
}

// requireLogin 校验 session cookie 的中间件（管理员与访客均放行，只读能力）
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

// requireAdmin 仅允许管理员会话（写操作/敏感操作）
func requireAdmin(db *sql.DB, next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		c, err := r.Cookie(sessionCookie)
		if err != nil || sessionRole(db, c.Value) != roleAdmin {
			http.Error(w, "forbidden", http.StatusForbidden)
			return
		}
		next(w, r)
	}
}