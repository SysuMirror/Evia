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
