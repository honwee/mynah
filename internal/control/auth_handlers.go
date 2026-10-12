package control

import (
	"errors"
	"net/http"

	"mynah/internal/auth"
)

func (s *Server) handleLogin(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Username string `json:"username"`
		Password string `json:"password"`
	}
	if !decode(w, r, &req) {
		return
	}
	token, mustChange, err := s.deps.Auth.Login(r.Context(), req.Username, req.Password)
	if err != nil {
		if errors.Is(err, auth.ErrBadCredentials) {
			fail(w, http.StatusUnauthorized, "invalid username or password")
		} else {
			logErr("login", err)
			fail(w, http.StatusInternalServerError, "login failed")
		}
		return
	}
	ok(w, map[string]any{"token": token, "must_change_password": mustChange})
}

func (s *Server) handleMe(w http.ResponseWriter, r *http.Request) {
	id := identity(r)
	ok(w, map[string]any{"id": id.ID, "username": id.Username})
}

func (s *Server) handlePassword(w http.ResponseWriter, r *http.Request) {
	var req struct {
		OldPassword string `json:"old_password"`
		NewPassword string `json:"new_password"`
	}
	if !decode(w, r, &req) {
		return
	}
	err := s.deps.Auth.ChangePassword(r.Context(), identity(r), req.OldPassword, req.NewPassword)
	if err != nil {
		if errors.Is(err, auth.ErrBadCredentials) {
			fail(w, http.StatusUnauthorized, "old password incorrect")
		} else {
			fail(w, http.StatusBadRequest, err.Error())
		}
		return
	}
	ok(w, map[string]any{"changed": true})
}
