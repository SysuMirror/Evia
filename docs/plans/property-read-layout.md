# Property read layout: deferred design

The compact winner columns and identity projections improve count and identity queries. Property filters, grouping and metrics still read the original `properties_json` String. No property-column migration or reader switch is delivered. Existing scan, memory and timeout budgets remain in force.

An isolated ClickHouse 26.8.36.3 prototype encoded decoded top-level UTF-8 keys as `p_` plus uppercase hexadecimal and stored each value as a String in native JSON. This avoids dotted-key collisions and Dynamic numeric inference; dedicated and shared-overflow paths can be selected individually. However, constructing those Strings with `JSONExtractKeysAndValuesRaw` does **not** preserve the original value tokens:

| Original value | Extracted raw text | Consequence after reconstruction |
| --- | --- | --- |
| `1e2` | `100` | `JSONType` changes from `Double` to `Int64` |
| `1.00` | `1` | Floating-point token becomes an integer token |
| `0.1234567890123456789` | `0.12345678901234568` | Decimal text is rounded |
| `[-0.0]` | `[-0]` | `Array(String)` extraction changes from `["-0"]` to `["0"]` |

`JSON_QUERY` also normalizes these values. Dynamic map extraction additionally rounded the integer `9007199254740993`. `simpleJSONExtractRaw` preserves lexical tokens but does not safely distinguish arbitrary escaped keys and nested matching keys. These methods cannot establish a lossless generic property read path. The prototype was removed; its read savings are not production capacity evidence.

The investigation also found an independent correctness bug: ClickHouse's `JSONType` reports floating-point values as `Double`, whereas the numeric field guard checked the SQL name `Float64`. The delivered guard correction includes decimal and exponent values in existing numeric metrics, comparisons and grouping, while still excluding missing, null, string, boolean, array and object values. This fix continues to use the original property document and does not change array extraction or introduce a new storage layout.

A future design must preserve each top-level value using a verified lossless extractor. One candidate is to derive an encoded map of per-property `json.RawMessage` tokens alongside the original document in the existing writer and commit both in the **same raw-table insert**. The original document remains authoritative; the map is a derived read layout. This requires an explicit layout version, strict decoded-key uniqueness and exact handling of missing, null, arrays, objects, integer precision, decimal/exponent tokens and signed zero. It must not add a second independently committed fact write.

Old writers and old parts require an explicit compatibility and readiness plan before switching readers. Historical backfill must use a verified lossless parser, rewrite bounded inspected partitions and retain the original documents. A successful ALTER acknowledgement or schema version alone does not prove every relevant part is materialized. Readers must retain the original path until the required layout is present and verified for the selected data; rollback must remain possible. A database-side extractor is another option only after lexical and query-result equivalence is demonstrated on the same edge cases.

One compatibility option to verify is a server-computed legacy-document column: layout version zero retains the original document, while a new writer atomically supplies version one and the losslessly derived values, making that legacy column empty. A reader would choose the legacy document or reconstructed requested values from the **same winning tuple**. This could allow old writers and unmaterialized parts to remain correct without requiring new parts to read the full original document. It still needs physical-read verification, omitted-field profile tests and constraints on the writer-owned layout marker. It is a design option, not an implemented fallback or permission to replace an active partition while ingestion continues.

Any future native JSON layout needs a finite per-part dynamic-path limit, correct shared-storage fallback and measurements on realistic key distributions. Validate conflicting winners, source and version boundaries, legacy and new writers, missing/null/false/zero, unsupported shapes and precision before measuring complete production queries under the existing budgets. Storage duplication, insert CPU, merge cost and bounded backfill cost must be included in that assessment.

## Landed foundation: lossless Go token deriver

The first building block now exists as `backend/internal/pipeline/propertylayout`. It is a pure, side-effect-free package with no writer or reader call sites yet, so it cannot change any stored or served data:

- `EncodeKey`/`DecodeKey` map a decoded top-level key to `p_` plus uppercase hexadecimal of its UTF-8 bytes and back. The transform is reversible and collision-free for dotted, spaced and non-ASCII keys.
- `DeriveMap` parses a canonical properties object into `map[encodedKey]json.RawMessage`. Values stay as their exact input tokens because `json.RawMessage` is never re-serialized through a numeric Go type; a streaming token pass first rejects duplicate decoded keys, non-objects, trailing data and malformed JSON, and the property cap of 50 is enforced.
- Unit tests pin the lexical edge cases from the table above (`1e2`, `1.00`, the long decimal, `-0.0`, the integer beyond the safe range) plus null/false/zero, arrays, nested objects, empty objects, token copy isolation and over-limit documents.

The remaining work, still gated behind real ClickHouse physical-read verification, is: add an explicit layout version and derived columns in a migration, have the writer commit the derived map atomically with the authoritative document in the same raw-table insert, build a reader that chooses the original document or reconstructed values from the same winning tuple while retaining fallback for old writers and unmaterialized parts, and then run a bounded backfill and the full query matrix under the existing budgets. The deriver alone is not a capacity claim.

## Landed storage: version 006 columns and atomic writer layout

The next two building blocks now exist, still without any reader switch:

- Migration `deploy/clickhouse/006_property_layout.sql` adds `property_layout_version UInt8 DEFAULT 0` and `property_tokens Map(String, String) DEFAULT map()` to `event_facts_raw`. The map keys are the `p_`-prefixed hexadecimal identifiers and the values are the exact JSON token texts. Sort and partition keys, historical parts and the `event_facts` view tuple are intentionally untouched, so applying the migration cannot change a served query result. Legacy parts and writers that omit the fields read version 0 and an empty map through type defaults.
- The fact writer now calls `propertylayout.DeriveMap` on the normalized document inside `rowFor` and commits `property_layout_version = 1` together with `property_tokens` in the same JSONEachRow insert as the authoritative `properties_json`; there is no second fact write. A derivation error fails the insert closed. The existing `input_format_skip_unknown_fields=0` profile makes a database that has not applied 006 reject the new writer instead of silently dropping the layout, and an integration test pins that fail-closed ordering between migrations 003 and 006.
- Integration evidence on ClickHouse 26.8.36.3 pins the stored map tokens byte-for-byte against the Go deriver for `1e2`, `1.00`, the long decimal, `-0.0`, `0`, `false`, strings and arrays inside the ingestion contract, confirms that an empty properties object legitimately stores version 1 with an empty map, and confirms a simulated pre-006 writer stores version 0 with an empty map while the view keeps serving its authoritative document. This is storage correctness evidence only; it is not a scan-byte or capacity claim.

## Landed view: version 007 exposes the layout on the deduplicated tuple

Migration `deploy/clickhouse/007_property_layout_view.sql` recreates `event_facts` with the original 26 argMin tuple slots unchanged and appends `property_layout_version` as fact.27 and `property_tokens` as fact.28. The winner weight is unchanged, so the layout always describes the same winning row as the exposed `properties_json`; legacy parts contribute the version-0/empty-map defaults. An isolated probe and an integration test both confirm that `argMin` carries the `Map(String, String)` coherently through the tuple, including the exact lexical tokens, and a view test pins two mixed-replay identities: a version-1 winner exposes its tokens while a version-0 winner keeps an empty map even when the losing replay carries version 1. No reader selects the new columns yet, so served results are unchanged; the full analysis and pipeline integration suites stay green.

The remaining gated work is the reader switch itself: make `planner.field` choose the token map for version-1 rows and fall back to the authoritative document for any other version, old writer or unmaterialized part, extending the trend winner subquery tuple the same way while keeping every property key a bound parameter; then measure physical reads, run a bounded Go-driven backfill and rerun the full query matrix under the existing scan, memory and timeout budgets. Rollback must keep the version-0 document path correct throughout.
