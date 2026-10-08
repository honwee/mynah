package control

import (
	"io"
	"net/http"
	"strconv"
	"time"

	"github.com/jackc/pgx/v5"

	"mynah/internal/knowledge"
	"mynah/internal/rag"
)

// registerKB is called from New when DB + ingester are present.
func (s *Server) registerKB() {
	s.private("GET /api/v1/kb", s.handleKBList)
	s.private("POST /api/v1/kb", s.handleKBCreate)
	s.private("PATCH /api/v1/kb/{id}", s.handleKBUpdate)
	s.private("DELETE /api/v1/kb/{id}", s.handleKBDelete)
	s.private("GET /api/v1/kb/{id}/documents", s.handleDocList)
	s.private("POST /api/v1/kb/{id}/documents", s.handleDocUpload)
	s.private("GET /api/v1/documents/{id}", s.handleDocGet)
	s.private("DELETE /api/v1/documents/{id}", s.handleDocDelete)
	s.private("POST /api/v1/documents/{id}/reindex", s.handleDocReindex)
	s.private("POST /api/v1/kb/search-test", s.handleSearchTest)
}

type kbRow struct {
	ID          int64     `json:"id"`
	Name        string    `json:"name"`
	Description string    `json:"description"`
	EmbedModel  string    `json:"embed_model"`
	EmbedDim    int       `json:"embed_dim"`
	Enabled     bool      `json:"enabled"`
	DocCount    int       `json:"doc_count"`
	CreatedAt   time.Time `json:"created_at"`
	UpdatedAt   time.Time `json:"updated_at"`
}

func (s *Server) handleKBList(w http.ResponseWriter, r *http.Request) {
	rows, err := s.deps.DB.Pool.Query(r.Context(), `
		SELECT kb.id, kb.name, kb.description, kb.embed_model, kb.embed_dim, kb.enabled,
		       (SELECT count(*) FROM documents d WHERE d.kb_id = kb.id), kb.created_at, kb.updated_at
		FROM knowledge_bases kb ORDER BY kb.id`)
	if err != nil {
		logErr("kb list", err)
		fail(w, http.StatusInternalServerError, "query failed")
		return
	}
	defer rows.Close()
	out := []kbRow{}
	for rows.Next() {
		var k kbRow
		if err := rows.Scan(&k.ID, &k.Name, &k.Description, &k.EmbedModel, &k.EmbedDim,
			&k.Enabled, &k.DocCount, &k.CreatedAt, &k.UpdatedAt); err != nil {
			fail(w, http.StatusInternalServerError, "scan failed")
			return
		}
		out = append(out, k)
	}
	ok(w, out)
}

func (s *Server) handleKBCreate(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Name        string `json:"name"`
		Description string `json:"description"`
	}
	if !decode(w, r, &req) {
		return
	}
	if req.Name == "" {
		fail(w, http.StatusBadRequest, "name is required")
		return
	}
	// The KB locks in the embedding model/dim active at creation.
	cfg := s.deps.Config.Current().RAG
	var k kbRow
	err := s.deps.DB.Pool.QueryRow(r.Context(), `
		INSERT INTO knowledge_bases (name, description, embed_model, embed_dim)
		VALUES ($1, $2, $3, $4)
		RETURNING id, name, description, embed_model, embed_dim, enabled, 0, created_at, updated_at`,
		req.Name, req.Description, cfg.EmbedModel, cfg.EmbedDim).
		Scan(&k.ID, &k.Name, &k.Description, &k.EmbedModel, &k.EmbedDim, &k.Enabled,
			&k.DocCount, &k.CreatedAt, &k.UpdatedAt)
	if err != nil {
		fail(w, http.StatusConflict, "create failed (duplicate name?): "+err.Error())
		return
	}
	ok(w, k)
}

func (s *Server) handleKBUpdate(w http.ResponseWriter, r *http.Request) {
	id, bad := pathID(w, r)
	if bad {
		return
	}
	var req struct {
		Name        *string `json:"name"`
		Description *string `json:"description"`
		Enabled     *bool   `json:"enabled"`
	}
	if !decode(w, r, &req) {
		return
	}
	tag, err := s.deps.DB.Pool.Exec(r.Context(), `
		UPDATE knowledge_bases SET
			name        = COALESCE($2, name),
			description = COALESCE($3, description),
			enabled     = COALESCE($4, enabled),
			updated_at  = now()
		WHERE id = $1`, id, req.Name, req.Description, req.Enabled)
	if err != nil {
		fail(w, http.StatusConflict, err.Error())
		return
	}
	if tag.RowsAffected() == 0 {
		fail(w, http.StatusNotFound, "knowledge base not found")
		return
	}
	ok(w, map[string]any{"updated": true})
}

func (s *Server) handleKBDelete(w http.ResponseWriter, r *http.Request) {
	id, bad := pathID(w, r)
	if bad {
		return
	}
	tag, err := s.deps.DB.Pool.Exec(r.Context(),
		`DELETE FROM knowledge_bases WHERE id = $1`, id) // cascades to documents+chunks
	if err != nil {
		logErr("kb delete", err)
		fail(w, http.StatusInternalServerError, "delete failed")
		return
	}
	if tag.RowsAffected() == 0 {
		fail(w, http.StatusNotFound, "knowledge base not found")
		return
	}
	ok(w, map[string]any{"deleted": true})
}

type docRow struct {
	ID         int64     `json:"id"`
	KBID       int64     `json:"kb_id"`
	Filename   string    `json:"filename"`
	SizeBytes  int64     `json:"size_bytes"`
	Status     string    `json:"status"`
	Error      string    `json:"error"`
	ChunkCount int       `json:"chunk_count"`
	CreatedAt  time.Time `json:"created_at"`
	UpdatedAt  time.Time `json:"updated_at"`
}

const docCols = `id, kb_id, filename, size_bytes, status, error, chunk_count, created_at, updated_at`

func scanDoc(row pgx.Row) (docRow, error) {
	var d docRow
	err := row.Scan(&d.ID, &d.KBID, &d.Filename, &d.SizeBytes, &d.Status, &d.Error,
		&d.ChunkCount, &d.CreatedAt, &d.UpdatedAt)
	return d, err
}

func (s *Server) handleDocList(w http.ResponseWriter, r *http.Request) {
	id, bad := pathID(w, r)
	if bad {
		return
	}
	rows, err := s.deps.DB.Pool.Query(r.Context(),
		`SELECT `+docCols+` FROM documents WHERE kb_id = $1 ORDER BY id`, id)
	if err != nil {
		logErr("doc list", err)
		fail(w, http.StatusInternalServerError, "query failed")
		return
	}
	defer rows.Close()
	out := []docRow{}
	for rows.Next() {
		d, err := scanDoc(rows)
		if err != nil {
			fail(w, http.StatusInternalServerError, "scan failed")
			return
		}
		out = append(out, d)
	}
	ok(w, out)
}

// handleDocUpload accepts multipart/form-data with a "file" part (.txt/.md,
// <=10MB), parses it, stores raw text as a pending document, and wakes the
// ingester. Returns the document immediately; poll GET /documents/{id} for
// status pending -> processing -> ready/failed.
func (s *Server) handleDocUpload(w http.ResponseWriter, r *http.Request) {
	kbID, bad := pathID(w, r)
	if bad {
		return
	}
	var enabled bool
	if err := s.deps.DB.Pool.QueryRow(r.Context(),
		`SELECT enabled FROM knowledge_bases WHERE id = $1`, kbID).Scan(&enabled); err != nil {
		fail(w, http.StatusNotFound, "knowledge base not found")
		return
	}
	if err := r.ParseMultipartForm(knowledge.MaxUploadBytes); err != nil {
		fail(w, http.StatusBadRequest, "multipart parse: "+err.Error())
		return
	}
	file, hdr, err := r.FormFile("file")
	if err != nil {
		fail(w, http.StatusBadRequest, `missing "file" form field`)
		return
	}
	defer file.Close()
	if hdr.Size > knowledge.MaxUploadBytes {
		fail(w, http.StatusRequestEntityTooLarge, "file exceeds 10MB")
		return
	}
	data, err := io.ReadAll(io.LimitReader(file, knowledge.MaxUploadBytes+1))
	if err != nil || len(data) > knowledge.MaxUploadBytes {
		fail(w, http.StatusRequestEntityTooLarge, "file exceeds 10MB")
		return
	}
	text, err := knowledge.Parse(hdr.Filename, data)
	if err != nil {
		fail(w, http.StatusBadRequest, err.Error())
		return
	}
	if text == "" {
		fail(w, http.StatusBadRequest, "file is empty")
		return
	}
	d, err := scanDoc(s.deps.DB.Pool.QueryRow(r.Context(), `
		INSERT INTO documents (kb_id, filename, raw_text, size_bytes)
		VALUES ($1, $2, $3, $4) RETURNING `+docCols,
		kbID, hdr.Filename, text, hdr.Size))
	if err != nil {
		logErr("doc insert", err)
		fail(w, http.StatusInternalServerError, "insert failed")
		return
	}
	s.deps.Ingester.Notify()
	ok(w, d)
}

func (s *Server) handleDocGet(w http.ResponseWriter, r *http.Request) {
	id, bad := pathID(w, r)
	if bad {
		return
	}
	d, err := scanDoc(s.deps.DB.Pool.QueryRow(r.Context(),
		`SELECT `+docCols+` FROM documents WHERE id = $1`, id))
	if err != nil {
		fail(w, http.StatusNotFound, "document not found")
		return
	}
	ok(w, d)
}

func (s *Server) handleDocDelete(w http.ResponseWriter, r *http.Request) {
	id, bad := pathID(w, r)
	if bad {
		return
	}
	tag, err := s.deps.DB.Pool.Exec(r.Context(),
		`DELETE FROM documents WHERE id = $1`, id) // cascades to chunks
	if err != nil {
		logErr("doc delete", err)
		fail(w, http.StatusInternalServerError, "delete failed")
		return
	}
	if tag.RowsAffected() == 0 {
		fail(w, http.StatusNotFound, "document not found")
		return
	}
	ok(w, map[string]any{"deleted": true})
}

func (s *Server) handleDocReindex(w http.ResponseWriter, r *http.Request) {
	id, bad := pathID(w, r)
	if bad {
		return
	}
	tag, err := s.deps.DB.Pool.Exec(r.Context(), `
		UPDATE documents SET status = 'pending', error = '', updated_at = now()
		WHERE id = $1 AND status IN ('ready', 'failed')`, id)
	if err != nil {
		logErr("doc reindex", err)
		fail(w, http.StatusInternalServerError, "reindex failed")
		return
	}
	if tag.RowsAffected() == 0 {
		fail(w, http.StatusConflict, "document not found or already queued")
		return
	}
	s.deps.Ingester.Notify()
	ok(w, map[string]any{"queued": true})
}

// handleSearchTest runs a retrieval with optional parameter overrides — the
// console's tuning playground. Does not touch the live rag config.
func (s *Server) handleSearchTest(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Query     string   `json:"query"`
		TopK      *int     `json:"top_k"`
		Threshold *float32 `json:"threshold"`
		Rerank    *bool    `json:"rerank"` // default: on when a reranker exists
	}
	if !decode(w, r, &req) {
		return
	}
	if req.Query == "" {
		fail(w, http.StatusBadRequest, "query is required")
		return
	}
	cfg := s.deps.Config.Current().RAG
	topK := cfg.TopK
	if req.TopK != nil {
		topK = *req.TopK
	}
	threshold := float32(cfg.Threshold)
	if req.Threshold != nil {
		threshold = *req.Threshold
	}
	reranker := s.reranker()
	useRerank := reranker != nil
	if req.Rerank != nil {
		useRerank = *req.Rerank && reranker != nil
	}
	fetchK := topK
	if useRerank {
		fetchK = cfg.CandidateCount(topK)
	}
	retriever := rag.NewRetriever(s.deps.DB, rag.NewEmbedder(cfg.EmbedURL, cfg.EmbedModel))
	start := time.Now()
	chunks, err := retriever.Retrieve(r.Context(), req.Query, fetchK, threshold)
	if err != nil {
		fail(w, http.StatusBadGateway, "retrieve: "+err.Error())
		return
	}
	reranked := false
	if useRerank && len(chunks) > 1 {
		out, err := reranker.Rerank(r.Context(), req.Query, chunks)
		if err != nil {
			logErr("search-test rerank (degraded to vector order)", err)
		} else {
			chunks = out
			reranked = true
		}
	}
	if len(chunks) > topK {
		chunks = chunks[:topK]
	}
	ok(w, map[string]any{
		"query": req.Query, "top_k": topK, "threshold": threshold,
		"reranked":   reranked,
		"latency_ms": time.Since(start).Milliseconds(),
		"chunks":     chunks,
	})
}

// pathID parses {id} from the route; on failure it writes a 400 and
// returns bad=true.
func pathID(w http.ResponseWriter, r *http.Request) (int64, bool) {
	id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil || id <= 0 {
		fail(w, http.StatusBadRequest, "invalid id")
		return 0, true
	}
	return id, false
}
