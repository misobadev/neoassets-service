-- Canonical genre catalog. A game's genre must be one of these names; the web
-- submission forms and the data importer both work with this list.
CREATE TABLE IF NOT EXISTS genres (
    id         TEXT PRIMARY KEY,
    name       TEXT NOT NULL UNIQUE,
    sort_order INT  NOT NULL DEFAULT 0
);

INSERT INTO genres (id, name, sort_order) VALUES
    ('action',        'Action',             1),
    ('adventure',     'Adventure',          2),
    ('beat-em-up',    'Beat ''em Up',       3),
    ('fighting',      'Fighting',           4),
    ('platformer',    'Platformer',         5),
    ('puzzle',        'Puzzle',             6),
    ('racing',        'Racing',             7),
    ('rpg',           'Role-Playing (RPG)', 8),
    ('shooter',       'Shooter',            9),
    ('shoot-em-up',   'Shoot ''em Up',     10),
    ('simulation',    'Simulation',        11),
    ('sports',        'Sports',            12),
    ('strategy',      'Strategy',          13),
    ('music',         'Music & Rhythm',    14),
    ('board-card',    'Board & Card',      15),
    ('pinball',       'Pinball',           16),
    ('educational',   'Educational',       17),
    ('quiz',          'Quiz',              18),
    ('compilation',   'Compilation',       19),
    ('miscellaneous', 'Miscellaneous',     20)
ON CONFLICT (id) DO NOTHING;
