CREATE TABLE messages (
    id            BIGSERIAL PRIMARY KEY,
    session_id    UUID        NOT NULL,
    created_at    TIMESTAMPTZ NOT NULL DEFAULT now(),
    ip_hash       TEXT        NOT NULL,
    user_agent    TEXT        NOT NULL DEFAULT '',
    question      TEXT        NOT NULL,
    answer        TEXT        NOT NULL DEFAULT '',
    -- answered: normal reply, refused: off-topic, error: upstream/internal failure
    status        TEXT        NOT NULL CHECK (status IN ('answered', 'refused', 'error')),
    -- true when the bot lacked the information (see [[MISSING]] markers in the knowledge base)
    knowledge_gap BOOLEAN     NOT NULL DEFAULT FALSE,
    model         TEXT        NOT NULL DEFAULT '',
    input_tokens  INTEGER     NOT NULL DEFAULT 0,
    output_tokens INTEGER     NOT NULL DEFAULT 0,
    latency_ms    INTEGER     NOT NULL DEFAULT 0,
    error         TEXT        NOT NULL DEFAULT ''
);

CREATE INDEX messages_session_created_idx ON messages (session_id, created_at);
CREATE INDEX messages_created_idx ON messages (created_at DESC);
CREATE INDEX messages_gap_idx ON messages (created_at DESC) WHERE knowledge_gap;
