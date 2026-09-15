-- +goose Up
-- Where Schall sends word that the review queue has a question, in the shape
-- listenbrainz_settings set (00046).
--
-- The review queue is the screen where a person answers what nothing could
-- decide: which copy of a recording to keep, what a file actually is. Its speed
-- is a correctness requirement rather than polish — a queue nobody empties is a
-- queue somebody eventually empties by accepting everything, and the guarantee
-- that Schall never guesses dies there while still looking alive in the code.
-- A queue can only be answered quickly if somebody knows it has something in it,
-- and today that is discovered by opening the app.
--
-- One row, one address. There is no list of recipients because there is one
-- person: this is a self-hosted collection manager, and a second endpoint is a
-- feature nobody has asked for.
--
-- Off by default and off after an upgrade. Nothing is sent anywhere until
-- somebody enters an address and switches it on, which is the only honest
-- default for a thing that talks to a server Schall was not told about before.
CREATE TABLE notification_settings (
    singleton BOOLEAN PRIMARY KEY DEFAULT true,

    -- Which kind of thing is at the other end. 'ntfy' is the push service whose
    -- server takes a plain text body and reads Title and Priority headers;
    -- 'webhook' is anything else, and is sent a JSON object. Both are one HTTP
    -- POST and neither needs a dependency.
    kind TEXT NOT NULL DEFAULT 'ntfy',

    -- The full address, topic and all — https://ntfy.sh/my-schall-topic. Schall
    -- does not assemble it from a host and a topic, because ntfy is commonly
    -- self-hosted behind a path and an assembled address would be wrong for
    -- exactly the people most likely to use this.
    endpoint TEXT NOT NULL DEFAULT '',

    -- A bearer token, for a topic that is not public. Optional: the ordinary
    -- ntfy topic needs none.
    token TEXT,

    enabled BOOLEAN NOT NULL DEFAULT false,

    -- What the last delivery came to, so the Settings page can say whether the
    -- address works without the user having to make a question appear in the
    -- queue to find out.
    connection_status TEXT NOT NULL DEFAULT 'unknown',
    connection_error TEXT,
    last_checked_at TIMESTAMPTZ,

    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT now(),

    CONSTRAINT notification_settings_single_row CHECK (singleton),
    CONSTRAINT notification_settings_kind_valid CHECK (kind IN ('ntfy', 'webhook')),
    -- Enabled with nowhere to send to is a setting that cannot do what it says.
    -- Disabled with an empty address is the row every installation starts with.
    CONSTRAINT notification_settings_endpoint_present
        CHECK (NOT enabled OR btrim(endpoint) <> ''),
    CONSTRAINT notification_settings_token_present
        CHECK (token IS NULL OR btrim(token) <> ''),
    CONSTRAINT notification_settings_status_valid
        CHECK (connection_status IN ('unknown', 'ok', 'failed'))
);

-- +goose Down
DROP TABLE IF EXISTS notification_settings;
