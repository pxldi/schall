-- +goose Up
-- What the ranking should prefer when it orders the copies peers are sharing,
-- and what it must never fetch, as the user's to say rather than the project's.
--
-- A source search asks a peer network for folders that might hold a release.
-- Every folder that comes back is a candidate, and Schall orders them by
-- format, completeness, availability and peer speed, recording a reason for
-- each part of the score. The format order was fixed in code: lossless first,
-- then good lossy. Somebody whose player cannot read FLAC, or whose volume is
-- small, or who cannot play DSF at all, had no way to say so — and the
-- acquisition loop takes the first candidate the ranking offers, so a
-- preference written in code decided what the library filled with.
--
-- One row, in the shape slskd_settings (00005) set and the settings rows since
-- have kept.
--
-- Preferences decide only what gets *tried*. Admission is unchanged: the audio
-- decides, a copy nobody could identify is held rather than imported, and no
-- preference has ever admitted or ever will admit a file. A preference also
-- never deletes: what a better copy of a recording the library already holds
-- should do is a separate question (#64).
CREATE TABLE source_preferences (
    singleton BOOLEAN PRIMARY KEY DEFAULT true,

    -- The formats to prefer, best first, as bare extensions in lower case:
    -- {mp3,flac}. Empty is the default and means "as Schall has always ordered
    -- them" — lossless above lossy, and lossy by bitrate. A format named here
    -- is preferred over every format that is not, in the order given; formats
    -- left out keep their usual order below them.
    preferred_formats TEXT[] NOT NULL DEFAULT '{}',

    -- The formats never to fetch, as bare extensions in lower case. This is a
    -- floor rather than a preference: a candidate whose format is named here is
    -- taken out of the results with its reason recorded, not ranked low and
    -- quietly picked when it is the only thing on offer.
    unacceptable_formats TEXT[] NOT NULL DEFAULT '{}',

    -- The lowest bitrate in kbps that a lossy copy may have. Zero is no floor,
    -- which is the default. It says nothing about lossless copies, which have
    -- no bitrate to compare, and nothing about a copy whose bitrate the peer
    -- did not state — an unknown bitrate is silence, and silence has never been
    -- evidence of anything here either.
    minimum_bitrate INTEGER NOT NULL DEFAULT 0,

    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT now(),

    CONSTRAINT source_preferences_single_row CHECK (singleton),
    CONSTRAINT source_preferences_bitrate_sane
        CHECK (minimum_bitrate >= 0 AND minimum_bitrate <= 3000),
    -- A format cannot be both preferred and refused: that is a setting that
    -- cannot do what it says, and guessing which half was meant is worse than
    -- refusing to store it.
    CONSTRAINT source_preferences_not_both
        CHECK (NOT (preferred_formats && unacceptable_formats))
);

-- +goose Down
DROP TABLE IF EXISTS source_preferences;
