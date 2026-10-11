-- +goose Up
ALTER TABLE sessions ADD COLUMN output_kind TEXT NOT NULL DEFAULT '';

-- +goose Down
ALTER TABLE sessions DROP COLUMN output_kind;
