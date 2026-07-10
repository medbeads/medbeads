# M1 完了基準の実測証拠(2026-07-10)

対象ストア: Synthea 1,135患者(生存1,000 + 死亡135)、960,443 Bead、
`~/medbeads-synthea/medbeads_data`(シード固定生成、bench/README.md 参照)。
計測コミット: 3d6f721。マシン: Apple Silicon (darwin/arm64)。

## 受け入れ検査(reviewer 推奨リスト)

| 検査 | 結果 |
|---|---|
| ingest 完走 | 1,135/1,135 患者、960,443 Bead、失敗0、parent_fallback 0(3h20m) |
| manifest 整合 | 行数 960,443 = total_beads、patient_root ユニーク 1,135、sha256: prefix 全行 |
| `medbeadsd verify` | 1,135 Pod / 960,443 フレーム 全 OK(CRC + sha256 再ハッシュ、6.8秒) |
| reindex 往復 | index.db 全削除→Pod 正本のみから再構築 5m40s、全カウント一致(beads / parent edges 959,308 / antigens 1,575,174 / rxnorm 45,954 / FTS) |
| 親エッジ抜き取り | observation/medicationrequest/condition の親は全量 fhir_encounter(root 誤結線 0) |

## 性能目標(docs/requirements.md §7)

| 目標 | 実測 | 判定 |
|---|---|---|
| 患者バンドル取得 <10ms | median **8.0ms**(n=100、シード固定サンプル) | **PASS** |
| FTS→患者解決 <50ms | median **19.6ms**(15クエリ×limit{20,50}) | **PASS** |
| context bundle p95 <500ms | MCP retrieve p95 **167.4ms**(n=100、viewer ロール = clearance コスト込み) | **PASS** |

参考値: 実データは mean 1,291.6 beads/患者(設計前提の ~900 より大)。~900 Bead 帯(800–1000)の
median は 12.6ms — 帯域判定は情報提供(ゲートは overall median)。最遅患者 12,260 Bead で 188ms。

生ログ: `m1_go_perf_1135.txt`(Go engine 実測)/ `m1_retrieve_1135.json`(MCP retrieve 分布)。
再現: `MEDBEADS_PERF_DATA=<store> CGO_ENABLED=1 go test -tags sqlite_fts5 ./internal/engine/ -run TestPerf -v`
および `uv run python -m bench.perf --data-dir <store> --fhir-dir <synthea fhir> --medbeadsd <bin>`。

## M1 完了基準(§9)との対応

- ゴールデンハッシュ一致: bead パッケージ(RFC 8785 ベクタ + 回帰ピン、9b18719)
- reindex 完全再構築: 本書の reindex 往復(96万 Bead、カウント一致)
- MCP retrieve 動作: stdio 実バイナリで実証(d90e6fe)+ 本書の p95 実測
- 1,000患者 ingest + 性能目標: 本書のとおり達成
- CI 緑: GitHub Actions 3ジョブ(go / bench / ui)全 success
