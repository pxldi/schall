-- +goose Up
-- What happens to the provider's copy after Schall has imported it is a policy
-- the user sets, not something the importer decides. The row is a singleton
-- like the other settings, and its absence means the non-destructive default.
CREATE TABLE import_settings (
    singleton BOOLEAN PRIMARY KEY DEFAULT true,
    source_retention TEXT NOT NULL DEFAULT 'keep',
    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    CONSTRAINT import_settings_single_row CHECK (singleton),
    CONSTRAINT import_settings_source_retention_valid
        CHECK (source_retention IN ('keep', 'delete'))
);

-- Removing the provider's copy is part of an import's history for the same
-- reason every pause is: it is the only durable record of what became of the
-- bytes the request was satisfied from. 'source_retained' is recorded when the
-- policy asked for removal and Schall refused, so a policy that appears to do
-- nothing can be read back as the reason it did nothing.
ALTER TABLE download_import_reviews
    DROP CONSTRAINT download_import_reviews_kind_valid;
ALTER TABLE download_import_reviews
    ADD CONSTRAINT download_import_reviews_kind_valid
        CHECK (kind IN (
            'paused', 'revalidated', 'imported', 'resolved', 'withdrawn',
            'source_removed', 'source_retained'
        ));

-- +goose Down
ALTER TABLE download_import_reviews
    DROP CONSTRAINT download_import_reviews_kind_valid;
DELETE FROM download_import_reviews
    WHERE kind IN ('source_removed', 'source_retained');
ALTER TABLE download_import_reviews
    ADD CONSTRAINT download_import_reviews_kind_valid
        CHECK (kind IN ('paused', 'revalidated', 'imported', 'resolved', 'withdrawn'));
DROP TABLE IF EXISTS import_settings;
