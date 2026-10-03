# Future evolution

Planned changes beyond the current build, each in a few sentences. The reasoning behind today's design is in [`DESIGN.md`](DESIGN.md).

## 1. Kafka on the beacon write path

Today the events endpoints insert straight into PostgreSQL within a 2 s budget and drop the event if that fails. Putting Kafka between the endpoints and the writers turns a database slowdown or outage into a delay instead of a loss, and absorbs traffic spikes without the API ever waiting on the database. The endpoint keeps answering 202 immediately after producing to a topic partitioned by site; consumer workers insert in batches with the same `ON CONFLICT DO NOTHING` statements, so at-least-once delivery stays correct and nothing about attribution changes.

## 2. Experiments on multiple pages, goals owned by experiments

An experiment currently runs on exactly one `url_path`. The next step is a list of paths or path patterns per experiment, matched by the same normaliser in Go and JavaScript and shipped in the payload, so a site-wide element such as a navigation bar needs one experiment rather than one per page. Alongside that, an experiment declares which goals count for it. A conversion beacon still names only the visitor and the goal, and the server attributes it to the visitor's exposed experiments as today, but only to those that list the goal, so a `checkout` goal no longer counts for a headline test that never cared about it.

## 3. Columnar storage for results

The results API aggregates straight from the PostgreSQL event tables, which means a full scan of an experiment's exposures and conversions on every dashboard view. That is fine at thousands of rows and poor at hundreds of millions. Events would also be written to a columnar store such as ClickHouse, fed from the same Kafka topics as the PostgreSQL writers, and the results API would query it instead: per-variant counts, per-goal breakdowns and future per-page or time-series views become cheap group-bys over compressed columns. PostgreSQL keeps configuration and stays the system of record for deduplication; the columnar store holds the analytical copy, and a mismatch between the two is a reconciliation job, not a correctness problem for assignment.
