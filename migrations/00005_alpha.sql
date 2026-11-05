-- +goose Up
SELECT 1; -- issue #280 race experiment, never merged to master
-- +goose Down
SELECT 1;
