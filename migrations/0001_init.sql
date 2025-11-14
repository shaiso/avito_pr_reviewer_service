CREATE TABLE teams (
    team_name TEXT PRIMARY KEY
);

CREATE TABLE users (
    user_id   TEXT PRIMARY KEY,
    username  TEXT NOT NULL,
    is_active BOOLEAN NOT NULL DEFAULT TRUE,
    team_name TEXT NOT NULL REFERENCES teams(team_name)
);

CREATE TABLE pull_requests (
    pull_request_id   TEXT PRIMARY KEY,
    pull_request_name TEXT NOT NULL,
    author_id         TEXT NOT NULL REFERENCES users(user_id),
    status            TEXT NOT NULL CHECK (status IN ('OPEN', 'MERGED')),
    need_more_reviewers BOOLEAN NOT NULL DEFAULT FALSE,
    created_at        TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    merged_at         TIMESTAMPTZ
);

CREATE TABLE pr_reviewers (
    pull_request_id TEXT NOT NULL REFERENCES pull_requests(pull_request_id) ON DELETE CASCADE,
    user_id         TEXT NOT NULL REFERENCES users(user_id),
    PRIMARY KEY (pull_request_id, user_id)
);

CREATE INDEX idx_users_team_active
    ON users(team_name, is_active);

CREATE INDEX idx_pr_reviewers_user
    ON pr_reviewers(user_id);