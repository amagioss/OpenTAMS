CREATE EXTENSION IF NOT EXISTS btree_gist;

CREATE TABLE sources (
    id          UUID PRIMARY KEY,
    format      TEXT NOT NULL,
    label       TEXT,
    description TEXT,
    created     TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated     TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE TABLE source_tags (
    source_id UUID NOT NULL REFERENCES sources(id) ON DELETE CASCADE,
    name      TEXT NOT NULL,
    value     JSONB NOT NULL,
    PRIMARY KEY (source_id, name)
);

CREATE TABLE flows (
    id                 UUID PRIMARY KEY,
    source_id          UUID NOT NULL REFERENCES sources(id),
    format             TEXT NOT NULL,
    codec              TEXT,
    container          TEXT,
    label              TEXT,
    description        TEXT,
    essence_parameters JSONB,
    container_mapping  JSONB,
    avg_bit_rate       BIGINT,
    max_bit_rate       BIGINT,
    segment_duration   TEXT,
    generation         BIGINT,
    metadata_version   BIGINT,
    read_only          BOOLEAN NOT NULL DEFAULT false,
    created            TIMESTAMPTZ NOT NULL DEFAULT now(),
    metadata_updated   TIMESTAMPTZ NOT NULL DEFAULT now(),
    segments_updated   TIMESTAMPTZ,
    timerange          TEXT
);

CREATE TABLE flow_tags (
    flow_id UUID NOT NULL REFERENCES flows(id) ON DELETE CASCADE,
    name    TEXT NOT NULL,
    value   JSONB NOT NULL,
    PRIMARY KEY (flow_id, name)
);

CREATE TABLE flow_collection (
    flow_id           UUID NOT NULL REFERENCES flows(id) ON DELETE CASCADE,
    item_id           UUID NOT NULL,
    role              TEXT,
    container_mapping JSONB,
    sort_order        INT NOT NULL DEFAULT 0,
    PRIMARY KEY (flow_id, item_id)
);

CREATE TABLE objects (
    id              TEXT PRIMARY KEY,
    timerange       TEXT,
    key_frame_count INT,
    ref_count       INT NOT NULL DEFAULT 0
);

CREATE TABLE segments (
    id               BIGSERIAL PRIMARY KEY,
    flow_id          UUID NOT NULL REFERENCES flows(id) ON DELETE CASCADE,
    object_id        TEXT NOT NULL REFERENCES objects(id),
    timerange        TEXT NOT NULL,
    lower_ns         BIGINT NOT NULL,
    upper_ns         BIGINT,
    ts_offset        TEXT NOT NULL DEFAULT '0:0',
    object_timerange TEXT,
    last_duration    TEXT,
    key_frame_count  INT,
    sample_offset    BIGINT,
    sample_count     BIGINT,
    get_urls         JSONB,
    created_at       TIMESTAMPTZ NOT NULL DEFAULT now(),
    CONSTRAINT no_segment_overlap EXCLUDE USING gist (
        flow_id WITH =,
        int8range(lower_ns, COALESCE(upper_ns, 9223372036854775807)) WITH &&
    )
);

CREATE INDEX segments_flow_lower ON segments (flow_id, lower_ns);
CREATE INDEX flows_source_id     ON flows (source_id);
CREATE INDEX flows_created       ON flows (created);
CREATE INDEX sources_created     ON sources (created);
