package api

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"log"
	"net/http"
	"os"
	"time"

	_ "github.com/jackc/pgx/v5"
)

type Server struct {
	db         *sql.DB
	adminToken string
}

func NewServer(db *sql.DB) *Server {
	token := os.Getenv("ADMIN_TOKEN")
	return &Server{db: db,
		adminToken: token,
	}
}

func (s *Server) requireAdmin(w http.ResponseWriter, r *http.Request) bool {
	if s.adminToken == "" {
		return true
	}
	token := r.Header.Get("x-Admin-token")
	if token == "" || token != s.adminToken {
		writeError(w, http.StatusUnauthorized, "UNAUTHORIZED", "invalid or missing admin token")
		return false
	}
	return true
}

func (s *Server) RegisterRoutes(mux *http.ServeMux) {
	mux.HandleFunc("/team/add", s.handleTeamAdd)
	mux.HandleFunc("/team/get", s.handleTeamGet)

	mux.HandleFunc("/users/setIsActive", s.handleUserSetIsActive)
	mux.HandleFunc("/users/getReview", s.handleUserGetReview)

	mux.HandleFunc("/pullRequest/create", s.handlePullRequestCreate)
	mux.HandleFunc("/pullRequest/merge", s.handlePullRequestMerge)
	mux.HandleFunc("/pullRequest/reassign", s.handlePullRequestReassign)

	mux.HandleFunc("/stats/reviewers", s.handleStatsReviewers)
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	enc := json.NewEncoder(w)
	enc.SetEscapeHTML(false)
	if err := enc.Encode(v); err != nil {
		log.Printf("writeJSON encode error: %v", err)
	}
}

func writeError(w http.ResponseWriter, status int, code, message string) {
	resp := ErrorResponse{
		Error: ErrorBody{
			Code:    code,
			Message: message,
		},
	}
	writeJSON(w, status, resp)
}

func withTimeout(ctx context.Context) (context.Context, context.CancelFunc) {
	return context.WithTimeout(ctx, 3*time.Second)
}

func timePtr(t time.Time) *string {
	s := t.UTC().Format(time.RFC3339)
	return &s
}

func (s *Server) loadReviewers(ctx context.Context, tx *sql.Tx, prID string) ([]string, error) {
	rows, err := tx.QueryContext(ctx, `
    SELECT user_id
    FROM pr_reviewers
    WHERE pull_request_id = $1
    ORDER BY user_id
    `, prID)

	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var reviewers []string
	for rows.Next() {
		var uid string
		if err := rows.Scan(&uid); err != nil {
			return nil, err
		}
		reviewers = append(reviewers, uid)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	return reviewers, nil
}

func (s *Server) handleTeamAdd(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		w.Header().Set("Allow", http.MethodPost)
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}

	var req Team
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "BAD_REQUEST", "invalid json body")
		return
	}

	if req.TeamName == "" {
		writeError(w, http.StatusBadRequest, "BAD_REQUEST", "team name is required")
		return
	}

	ctx, cancel := withTimeout(r.Context())
	defer cancel()

	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		log.Printf("begin tx: %v", err)
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}

	defer func() {
		_ = tx.Rollback()
	}()

	// проверяем существует ли команда
	var existing string
	err = tx.QueryRowContext(ctx, `SELECT team_name FROM teams WHERE team_name = $1`, req.TeamName).Scan(&existing)
	if err == nil {
		writeError(w, http.StatusBadRequest, "TEAM_EXISTS", "team name already exists")
		return
	}
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		log.Printf("check team exists: %v", err)
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}
	// вставляем команду
	_, err = tx.ExecContext(ctx, `INSERT INTO teams (team_name) Values ($1)`, req.TeamName)
	if err != nil {
		log.Printf("insert team: %v", err)
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}

	for _, m := range req.Members {
		if m.UserID == "" || m.Username == "" {
			writeError(w, http.StatusBadRequest, "BAD_REQUEST", "member user_id and username are required")
			return
		}

		_, err = tx.ExecContext(ctx, `
		INSERT INTO users (user_id, username, is_active, team_name)
		VALUES ($1, $2, $3, $4)
		ON CONFLICT (user_id) DO UPDATE
		SET username = EXCLUDED.username,
			is_active = EXCLUDED.is_active,
			team_name = EXCLUDED.team_name`, m.UserID, m.Username, m.IsActive, req.TeamName)
		if err != nil {
			log.Printf("upsert user %s: %v", m.UserID, err)
			http.Error(w, "internal error", http.StatusInternalServerError)
			return
		}
	}

	if err := tx.Commit(); err != nil {
		log.Printf("commit tx: %v", err)
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}

	resp := map[string]any{
		"team": req,
	}
	writeJSON(w, http.StatusCreated, resp)
}

func (s *Server) handleTeamGet(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		w.Header().Set("Allow", http.MethodGet)
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}

	teamName := r.URL.Query().Get("team_name")
	if teamName == "" {
		writeError(w, http.StatusBadRequest, "BAD_REQUEST", "team_name query parameter is required")
		return
	}

	ctx, cancel := withTimeout(r.Context())
	defer cancel()

	var existing string
	err := s.db.QueryRowContext(ctx, `SELECT team_name FROM teams WHERE team_name = $1`, teamName).Scan(&existing)
	if errors.Is(err, sql.ErrNoRows) {
		writeError(w, http.StatusNotFound, "NOT_FOUND", "team not found")
		return
	}

	if err != nil {
		log.Printf("select team members: %v", err)
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}

	rows, err := s.db.QueryContext(ctx, `
	SELECT user_id, username, is_active
	FROM users
	WHERE team_name = $1
	ORDER BY user_id`,
		teamName)

	if err != nil {
		log.Printf("select team members: %v", err)
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}

	defer rows.Close()

	var members []TeamMember
	for rows.Next() {
		var m TeamMember
		if err := rows.Scan(&m.UserID, &m.Username, &m.IsActive); err != nil {
			log.Printf("scan member: %v", err)
			http.Error(w, "internal error", http.StatusInternalServerError)
			return
		}
		members = append(members, m)
	}

	if err := rows.Err(); err != nil {
		log.Printf("rows err: %v", err)
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}

	resp := Team{
		TeamName: teamName,
		Members:  members,
	}

	writeJSON(w, http.StatusOK, resp)
}

func (s *Server) handleUserSetIsActive(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		w.Header().Set("Allow", http.MethodPost)
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}

	if ok := s.requireAdmin(w, r); !ok {
		return
	}

	var body struct {
		UserID   string `json:"user_id"`
		IsActive bool   `json:"is_active"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		writeError(w, http.StatusBadRequest, "BAD_REQUEST", "invalid json body")
		return
	}

	if body.UserID == "" {
		writeError(w, http.StatusBadRequest, "BAD_REQUEST", "user id is required")
		return
	}

	ctx, cancel := withTimeout(r.Context())
	defer cancel()

	var u User
	err := s.db.QueryRowContext(ctx, `
    UPDATE users
    SET is_active = $2
    WHERE user_id = $1
    RETURNING user_id, username, team_name, is_active
    `, body.UserID, body.IsActive).Scan(&u.UserID, &u.Username, &u.TeamName, &u.IsActive)

	if errors.Is(err, sql.ErrNoRows) {
		writeError(w, http.StatusNotFound, "NOT_FOUND", "user not found")
		return
	}

	if err != nil {
		log.Printf("setIsActive update: %v", err)
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}

	resp := map[string]any{
		"user": u,
	}
	writeJSON(w, http.StatusOK, resp)
}

func (s *Server) handleUserGetReview(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		w.Header().Set("Allow", http.MethodGet)
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}

	userID := r.URL.Query().Get("user_id")
	if userID == "" {
		writeError(w, http.StatusBadRequest, "BAD_REQUEST", "user_id query parameter is required")
		return
	}

	ctx, cancel := withTimeout(r.Context())
	defer cancel()

	var existing string
	err := s.db.QueryRowContext(ctx, `SELECT user_id FROM users WHERE user_id = $1`, userID).Scan(&existing)
	if errors.Is(err, sql.ErrNoRows) {
		writeError(w, http.StatusNotFound, "NOT_FOUND", "user not found")
		return
	}

	if err != nil {
		log.Printf("select user: %v", err)
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}

	rows, err := s.db.QueryContext(ctx, `
    SELECT p.pull_request_id, p.pull_request_name, p.author_id, p.status
    FROM pull_requests p
    JOIN pr_reviewers r ON r.pull_request_id = p.pull_request_id
    WHERE r.user_id = $1
    ORDER BY p.pull_request_id
    `, userID)

	if err != nil {
		log.Printf("select user reviews: %v", err)
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}

	defer rows.Close()

	var prs []PullRequestShort
	for rows.Next() {
		var pr PullRequestShort
		if err := rows.Scan(&pr.PullRequestID, &pr.PullRequestName, &pr.AuthorID, &pr.Status); err != nil {
			log.Printf("scan pr: %v", err)
			http.Error(w, "internal error", http.StatusInternalServerError)
			return
		}
		prs = append(prs, pr)
	}

	if err := rows.Err(); err != nil {
		log.Printf("rows err: %v", err)
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}

	resp := UserReviewsResponse{
		UserID:       userID,
		PullRequests: prs,
	}
	writeJSON(w, http.StatusOK, resp)
}

func (s *Server) handlePullRequestCreate(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		w.Header().Set("Allow", http.MethodPost)
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}

	if ok := s.requireAdmin(w, r); !ok {
		return
	}

	var body struct {
		PullRequestID   string `json:"pull_request_id"`
		PullRequestName string `json:"pull_request_name"`
		AuthorID        string `json:"author_id"`
	}

	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		writeError(w, http.StatusBadRequest, "BAD_REQUEST", "invalid json body")
		return
	}

	if body.PullRequestID == "" || body.PullRequestName == "" || body.AuthorID == "" {
		writeError(w, http.StatusBadRequest, "BAD_REQUEST", "pull request id or pull request name and author id are required")
		return
	}

	ctx, cancel := withTimeout(r.Context())
	defer cancel()

	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		log.Printf("begin tx: %v", err)
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}
	defer func() {
		_ = tx.Rollback()
	}()

	var existing string
	err = tx.QueryRowContext(ctx, `
    SELECT pull_request_id
    FROM pull_requests
    WHERE pull_request_id = $1
    `, body.PullRequestID).Scan(&existing)

	if err == nil {
		writeError(w, http.StatusConflict, "PR_EXISTS", "PR id already exists")
		return
	}
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		log.Printf("check pr exists: %v", err)
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}

	var authorTeam string
	err = tx.QueryRowContext(ctx, `
    SELECT team_name
    FROM users
    WHERE user_id = $1
    `, body.AuthorID).Scan(&authorTeam)
	if errors.Is(err, sql.ErrNoRows) {
		writeError(w, http.StatusNotFound, "NOT_FOUND", "user not found")
		return
	}

	if err != nil {
		log.Printf("select author: %v", err)
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}

	rows, err := tx.QueryContext(ctx, `
    SELECT user_id
    FROM users
    WHERE team_name = $1
    AND is_active = TRUE
    AND user_id <> $2
    ORDER BY random()
    LIMIT 2
    `, authorTeam, body.AuthorID)

	if err != nil {
		log.Printf("select reviewers: %v", err)
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}

	defer rows.Close()

	var reviewers []string
	for rows.Next() {
		var uid string
		if err := rows.Scan(&uid); err != nil {
			log.Printf("scan reviewer: %v", err)
			http.Error(w, "internal error", http.StatusInternalServerError)
			return
		}
		reviewers = append(reviewers, uid)
	}
	if err := rows.Err(); err != nil {
		log.Printf("rows err: %v", err)
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}

	needMore := len(reviewers) < 2

	var createdAt time.Time
	err = tx.QueryRowContext(ctx, `
    INSERT INTO pull_requests (pull_request_id, pull_request_name, author_id, status, need_more_reviewers)
    Values ($1, $2, $3, 'OPEN', $4) 
    RETURNING created_at
    `, body.PullRequestID, body.PullRequestName, body.AuthorID, needMore).Scan(&createdAt)

	if err != nil {
		log.Printf("insert pr: %v", err)
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}

	for _, uid := range reviewers {
		_, err = tx.ExecContext(ctx, `
        INSERT INTO pr_reviewers (pull_request_id, user_id)
        Values ($1, $2)
        `, body.PullRequestID, uid)

		if err != nil {
			log.Printf("insert pr_revievers (%s): %v", uid, err)
			http.Error(w, "internal error", http.StatusInternalServerError)
			return
		}
	}

	if err := tx.Commit(); err != nil {
		log.Printf("commit tx: %v", err)
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}

	pr := PullRequest{
		PullRequestID:     body.PullRequestID,
		PullRequestName:   body.PullRequestName,
		AuthorID:          body.AuthorID,
		Status:            "OPEN",
		AssignedReviewers: reviewers,
		NeedMoreReviewers: needMore,
		CreatedAt:         timePtr(createdAt),
		MergedAt:          nil,
	}

	resp := map[string]any{
		"pr": pr,
	}
	writeJSON(w, http.StatusCreated, resp)
}

func (s *Server) handlePullRequestMerge(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		w.Header().Set("Allow", http.MethodPost)
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}

	if ok := s.requireAdmin(w, r); !ok {
		return
	}

	var body struct {
		PullRequestID string `json:"pull_request_id"`
	}

	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		writeError(w, http.StatusBadRequest, "BAD_REQUEST", "invalid json body")
		return
	}

	if body.PullRequestID == "" {
		writeError(w, http.StatusBadRequest, "BAD_REQUEST", "pull_request_id is required")
		return
	}

	ctx, cancel := withTimeout(r.Context())
	defer cancel()

	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		log.Printf("begin tx: %v", err)
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}
	defer func() {
		_ = tx.Rollback()
	}()

	var (
		name        string
		authorID    string
		status      string
		needMore    bool
		createdAt   time.Time
		mergedAtSql sql.NullTime
	)

	err = tx.QueryRowContext(ctx, `
	SELECT pull_request_name, author_id, status, need_more_reviewers, created_at, merged_at
	FROM pull_requests
	Where pull_request_id = $1
	FOR UPDATE
	`, body.PullRequestID).Scan(&name, &authorID, &status, &needMore, &createdAt, &mergedAtSql)
	if errors.Is(err, sql.ErrNoRows) {
		writeError(w, http.StatusNotFound, "NOT_FOUND", "pull request not found")
		return
	}
	if err != nil {
		log.Printf("select pr: %v", err)
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}

	var mergedAt time.Time
	if status == "OPEN" {
		err = tx.QueryRowContext(ctx, `
		UPDATE pull_requests 
		SET status = 'MERGED', merged_at = NOW()
		WHERE pull_request_id = $1
		RETURNING merged_at`, body.PullRequestID).Scan(&mergedAt)

		if err != nil {
			log.Printf("update pt MERGED: %v", err)
			http.Error(w, "internal error", http.StatusInternalServerError)
			return
		}
		status = "MERGED"
	} else {
		if mergedAtSql.Valid {
			mergedAt = mergedAtSql.Time
		}
	}

	reviewers, err := s.loadReviewers(ctx, tx, body.PullRequestID)
	if err != nil {
		log.Printf("load reviewers: %v", err)
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}

	if err := tx.Commit(); err != nil {
		log.Printf("commit tx: %v", err)
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}

	pr := PullRequest{
		PullRequestID:     body.PullRequestID,
		PullRequestName:   name,
		AuthorID:          authorID,
		Status:            status,
		AssignedReviewers: reviewers,
		NeedMoreReviewers: needMore,
		CreatedAt:         timePtr(createdAt),
	}
	if !mergedAt.IsZero() {
		pr.MergedAt = timePtr(mergedAt)
	}

	resp := map[string]any{
		"pr": pr,
	}
	writeJSON(w, http.StatusOK, resp)
}

func (s *Server) handlePullRequestReassign(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		w.Header().Set("Allow", http.MethodPost)
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}

	if ok := s.requireAdmin(w, r); !ok {
		return
	}

	var body struct {
		PullRequestID string `json:"pull_request_id"`
		OldUserID     string `json:"old_user_id"`
	}

	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		writeError(w, http.StatusBadRequest, "BAD_REQUEST", "invalid json body")
		return
	}
	if body.PullRequestID == "" || body.OldUserID == "" {
		writeError(w, http.StatusBadRequest, "BAD_REQUEST", "pull_request_id and old_user_id is required")
		return
	}

	ctx, cancel := withTimeout(r.Context())
	defer cancel()

	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		log.Printf("begin tx: %v", err)
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}

	defer func() {
		_ = tx.Rollback()
	}()

	var (
		name        string
		authorID    string
		status      string
		needMore    bool
		createdAt   time.Time
		mergedAtSql sql.NullTime
	)

	err = tx.QueryRowContext(ctx, `
    SELECT pull_request_name, author_id, status, need_more_reviewers, created_at, merged_at
    FROM pull_requests
    WHERE pull_request_id = $1
    FOR UPDATE
    `, body.PullRequestID).Scan(&name, &authorID, &status, &needMore, &createdAt, &mergedAtSql)

	if errors.Is(err, sql.ErrNoRows) {
		writeError(w, http.StatusNotFound, "NOT_FOUND", "pull request not found")
		return
	}

	if err != nil {
		log.Printf("select pr: %v", err)
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}

	if status == "MERGED" {
		writeError(w, http.StatusConflict, "PR_MERGED", "cannot reassign on merged pull request")
		return
	}

	var oldTeam string
	err = tx.QueryRowContext(ctx, `
    SELECT team_name
    FROM users
    WHERE user_id = $1
    `, body.OldUserID).Scan(&oldTeam)

	if errors.Is(err, sql.ErrNoRows) {
		writeError(w, http.StatusNotFound, "NOT_FOUND", "user not found")
		return
	}

	if err != nil {
		log.Printf("select old user: %v", err)
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}

	var dummy int
	err = tx.QueryRowContext(ctx, `
    SELECT 1 
    FROM pr_reviewers
    WHERE pull_request_id = $1 AND user_id = $2
    `, body.PullRequestID, body.OldUserID).Scan(&dummy)

	if errors.Is(err, sql.ErrNoRows) {
		writeError(w, http.StatusConflict, "NOT_ASSIGNED", "reviewer is not assigned to this PR")
		return
	}

	if err != nil {
		log.Printf("check assigned: %v", err)
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}

	currentReviewers, err := s.loadReviewers(ctx, tx, body.PullRequestID)
	if err != nil {
		log.Printf("load reviewers: %v", err)
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}

	var otherReviewer string
	if len(currentReviewers) == 2 {
		if currentReviewers[0] == body.OldUserID {
			otherReviewer = currentReviewers[1]
		} else if currentReviewers[1] == body.OldUserID {
			otherReviewer = currentReviewers[0]
		} else {
			otherReviewer = currentReviewers[0]
		}
	}

	var newUserID string
	if otherReviewer != "" {
		err = tx.QueryRowContext(ctx, `
        SELECT user_id
        FROM users
        WHERE team_name = $1
        AND is_active = TRUE
        AND user_id <> $2
        AND user_id <> $3
        AND user_id <> $4
        ORDER BY random()
        LIMIT 1`, oldTeam, body.OldUserID, authorID, otherReviewer).Scan(&newUserID)

	} else {
		err = tx.QueryRowContext(ctx, `
        SELECT user_id
        FROM users
        WHERE team_name = $1
        AND is_active = TRUE
        AND user_id <> $2
        AND user_id <> $3
        ORDER BY random()
        LIMIT 1
        `, oldTeam, body.OldUserID, authorID).Scan(&newUserID)
	}

	if errors.Is(err, sql.ErrNoRows) {
		writeError(w, http.StatusConflict, "NO_CANDIDATE", "no active replacement candidate in team")
		return
	}

	if err != nil {
		log.Printf("select replacement: %v", err)
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}

	_, err = tx.ExecContext(ctx, `
    DELETE FROM pr_reviewers
    WHERE pull_request_id = $1 AND user_id = $2
    `, body.PullRequestID, body.OldUserID)

	if err != nil {
		log.Printf("delete old reviewers: %v", err)
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}

	_, err = tx.ExecContext(ctx, `
    INSERT INTO pr_reviewers (pull_request_id, user_id)
    VALUES ($1, $2)
    `, body.PullRequestID, newUserID)

	if err != nil {
		log.Printf("insert new reviewer: %v", err)
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}

	newReviewers, err := s.loadReviewers(ctx, tx, body.PullRequestID)
	if err != nil {
		log.Printf("load reviewers: %v", err)
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}

	var mergedAt *string
	if mergedAtSql.Valid {
		mergedAt = timePtr(mergedAtSql.Time)
	}

	if err := tx.Commit(); err != nil {
		log.Printf("commit tx: %v", err)
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}

	pr := PullRequest{
		PullRequestID:     body.PullRequestID,
		PullRequestName:   name,
		AuthorID:          authorID,
		Status:            status,
		AssignedReviewers: newReviewers,
		NeedMoreReviewers: needMore,
		CreatedAt:         timePtr(createdAt),
		MergedAt:          mergedAt,
	}

	resp := map[string]any{
		"pr":          pr,
		"replaced_by": newUserID,
	}
	writeJSON(w, http.StatusOK, resp)
}

func (s *Server) handleStatsReviewers(w http.ResponseWriter, r *http.Request) {
	if r.Method != "GET" {
		w.Header().Set("Allow", http.MethodGet)
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}

	if ok := s.requireAdmin(w, r); !ok {
		return
	}

	ctx, cancel := withTimeout(r.Context())
	defer cancel()

	rows, err := s.db.QueryContext(ctx, `
    SELECT u.user_id, u.username, 
    COUNT(r.pull_request_id) AS assignments
    FROM users u
    LEFT JOIN pr_reviewers r ON r.user_id = u.user_id
    GROUP BY u.user_id, u.username
    ORDER BY assignments DESC, u.user_id
    `)

	if err != nil {
		log.Printf("stats reviewers query: %v", err)
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}

	defer rows.Close()

	var items []ReviewerStatsItem
	for rows.Next() {
		var it ReviewerStatsItem
		if err := rows.Scan(&it.UserID, &it.Username, &it.Assignments); err != nil {
			log.Printf("stats reviewers scan: %v", err)
			http.Error(w, "internal error", http.StatusInternalServerError)
			return
		}
		items = append(items, it)
	}
	if err := rows.Err(); err != nil {
		log.Printf("stats reviewers rows: %v", err)
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}
	resp := ReviewersStatsResponse{
		Items: items,
	}
	writeJSON(w, http.StatusOK, resp)
}
