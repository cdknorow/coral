# Migration fixtures (#5392)

`<tag>/sessions.sql` here and `internal/board/testdata/migration/<tag>/messageboard.sql` are `sqlite3 .dump` of the databases the release `<tag>` itself creates:
its own code opened a fresh data folder (so the schema is exactly what that release made), a few rows were added through SQL (3 live sessions, 2 settings, 5
call metrics; 2 board subscribers, 4 messages, 3 tasks, all with obvious `fixture` names), and the files were checkpointed and vacuumed. No live data, no keys.
Text, not the `.db` files, so a patch carries them and a diff shows a change.

`TestMigrationChainFromOldReleases` (in `internal/store` and in `internal/board`) loads each dump into a temp file, opens it with the current code, and checks that
the schema has every table, column and index a new install has, that the rows and their key fields survive, and that a second open changes nothing.

To add a release: check out its tag, open a fresh data folder with that tag's `store.OpenWithKey` and `board.NewStoreWithKey`, insert the same rows, and run
`sqlite3 <file> .dump`. Do not generate one from the current code: the point is the old schema.
