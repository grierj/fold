-- +goose Up
-- Covers are upload-only (Oct 14 decision): clear any external cover link.
UPDATE groups SET image_url = '' WHERE image_url <> '' AND image_url NOT LIKE '/uploads/%';

-- +goose Down
SELECT 1;
