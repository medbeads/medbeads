# DESIGN v3.1 — 事実と解釈の二層分離

作成: 2026-07-10 / ステータス: **承認済み設計(peer レビュー2件 + ユーザー裁定を反映した v3.1.1)**
背景: 3視点批判レビュー(2026-07-10)への応答。北極星: 「電子カルテ(人が書くメモ)に
対抗できる、生成AI のためのカルテ」。peer レビュー: data-reviewer(条件付きGO) +
Codex(条件付きGO)— 両者の条件を本版で解決済み。

## 0. 決定事項(ユーザー合意)

1. antigens をハッシュ対象から除外し、**Bead 構造体から完全撤去**(タグは投影のみの存在)
2. sibling_link を Bead から index 投影へ降格(v2 で Bead 化した理由 = 検索速度は、
   患者パーティション + 複合索引の実測で解消済み)
3. 免疫メタファーは仕様書・論文の説明装置として維持、API/スキーマ語彙は標準語へ
4. clinical_note / assessment / attestation 型 + amends/retracts フィールドを**ハッシュ規約に
   今回焼き込む**。AI 起草→医師承認のワークフロー実装は将来機能(器だけ先行)
5. **未承認(unattested)Bead は retrieve 既定で除外**(明示フラグ時のみ強いラベル付きで返す)
6. **訂正・承認の関係は参照先の閲覧制限を継承**(参照先が見えないロールには関係も見せない。
   既存リンクの漏洩対策と同一原則。監査ロールには全可視)
7. 着手はスキーマ改定(U1)から。破壊的変更を一括し再 ingest は1回

## 1. 原則(強化)

- **正本(Pod)= 不変の臨床事実と人・AI の記述のみ**(検査値・処方・イベント・自由記述・
  アセスメント・承認・訂正)。**知識の実体(タグ辞書・リンクルール)も content-addressed な
  Bead として `_shared` に保存する** — 「どの知識で解釈したか」自体が改ざん不能な事実になる
- **投影(index)= 解釈のすべて**(タグ・リンク・状態・要約・検索構造)。辞書/ルール Bead の
  ID で世代管理され、常に正本から分オーダーで再構築可能
- **日次成長への増分追随を第一級の設計目標に**(GraphRAG 差別化の柱): 記録の追加 =
  患者内増分スキャン(ミリ秒〜秒)。知識の更新 = 投影の再構築(分)。グローバルな
  クラスタリング等の全体演算を持ち込まない
- 決定論的再構築は**入力の凍結保存とセットでのみ成立**する(教訓): 正本 + 辞書/ルール Bead +
  投影コード版の3点が揃って初めて「時点 T の解釈」を再計算できる

## 2. Bead v3.1(ハッシュ規約)

```
ID = sha256( JCS({ type, timestamp, author, parents, amends, retracts, content, evidence }) )
```

- **antigens 削除**(構造体からも撤去。タグ抽出は index 投影時に content から決定論実行)
- **amends: [bead_id]**(任意、dedup+辞書順 = parents と同じ正規化): 訂正。訂正後の内容は
  この Bead 自身の content。**訂正理由は content 内の構造化規約**(reason_code / reason_text)
- **retracts: [bead_id]**(任意): 取り消し(entered-in-error)。内容置換を伴わない純粋な
  取り消しは専用型 `retraction`(content = reason_code / reason_text / authorized_by)で行う
- **循環参照は構造的に不可能**(content-hash の前方参照不能性: A の ID 計算には B の ID が
  確定している必要があるため、相互訂正は物理的に作れない)。仕様として明記し検証は不要
- **チェーン/フォークの解決規則(決定論)**: 同一 Bead への複数訂正は
  `recorded_at → bead ID 辞書順` で最新有効を選ぶ。retracted は最強(amends より優先)。
  未承認の訂正は current にならない。retracted な Bead への amends は無効(status は
  retracted のまま)。cross-patient の amends/retracts は禁止(ingest 時拒否)
- **recorded_at はハッシュ外**(フレーム meta、書き込み時自動付与)。理由: ハッシュに含めると
  同一事実の再 ingest で ID が変わり、冪等性・クリーンスレート再構築が崩れる。medico-legal な
  保護はフレーム CRC(meta を含む)+ 将来の署名で担保
- parents = 「臨床文脈上の親」に純化。**patient_root 導出規則は従来維持**
  (registration=root / 親から継承 / 複数root・親なし=shared)。cross-patient parents は
  原則禁止、shared Bead(薬剤マスタ・辞書等)への参照は evidence フィールドで行う

### 新しい Bead 型

| type | content 規約 | 備考 |
|---|---|---|
| `clinical_note` | raw_text(**順序保存・無加工**)/ sections[](任意: S,O,A,P 等)/ source_system / source_document_id / language | 医師メモ・既存カルテ取り込み。抽出タグ・要約は投影側 |
| `assessment` | narrative / differential[] / plan(任意)/ reason 構造 | parents = 根拠 Bead 群(multi-parent DAG の本来の用途) |
| `attestation` | verdict(approved/rejected)/ scope / comment / attester_role | parents = [承認対象]。複数 attestation は「最新有効」を状態導出で選ぶ |
| `retraction` | reason_code / reason_text / authorized_by | retracts フィールドで対象を指す |
| `dictionary` / `link_rule` | 辞書・ルールの実体(JSON) | `_shared` に保存。投影世代が ID で参照 |

## 3. 投影層 v3.1(index — すべて再構築可能)

| テーブル | 旧名 | 要点 |
|---|---|---|
| `bead_tags` | bead_antigens | tag, bead_id, patient_root。**投影時に antigen.Extract 相当を content から実行**。再投影は置換方式(世代共存させない。過去世代は辞書 Bead + 決定論再計算で再現) |
| `clinical_links` | sibling_pairs + sibling エッジ | 投影として生成。relation(typed)/ severity / **evidence_basis**(cooccurrence \| curated_knowledge \| guideline)/ score_breakdown(JSON)/ rule_id / rule_version(=rule Bead ID)/ projection_run_id |
| `bead_status` | (新規) | active / amended / retracted / unattested の導出(§2 の解決規則で決定論)。FHIR 軸とは分離: record_status(本表)と FHIR clinicalStatus / verificationStatus(content 由来、active_views 用)を同一視しない |
| `active_conditions` / `active_medications` | (新規) | 表示ビュー。FHIR 原 status を保持し導出理由(rule_id)付き |
| `projection_manifest` | (新規) | **追記専用の世代台帳**: run_id PK / projection_name / code_version(git)/ dictionary_bead_id / config_hash / input_watermarks / built_at / activated_at / superseded_at |

- retrieve の provenance に **projection_run_id を必ず含める**(「時点 T に AI が見た解釈は
  どの世代か」を1値で特定)
- reindex = 2パス(①正本走査で beads/tags 等 → ②link projector)。従来の「Pod のみから
  完全再構築」不変条件を維持
- **クリアランス継承(ユーザー裁定)**: bead_status / clinical_links / attestation /
  active_views を返す全経路で「参照先 Bead が不可視なら関係も不可視」(既存 get_sibling_links
  の drop 原則を一般化)

## 4. リンク生成(APC 改め link projector)

- LOINC 同一コード・temporal 単独をトリガーから除外(87% ノイズの根絶)
- relation / severity はルール Bead から導出。**共起ベースは常に severity=info 固定、
  warning 以上は curated_knowledge(出典 ID 必須)に限定**
- 二次応答は廃止。暴走防止の仕分け: generation 減衰(廃止)/ UNIQUE・max_links_per_bead・
  IDF フィルタ・レートリミット(投影側に移設して維持)
- 増分性: 患者内ウォーターマーク方式を維持(bead_tags ベース)。辞書/ルール更新時は
  投影のみ再構築

## 5. API(契約の境界を明確化)

- **正本と投影を別オブジェクトに**: `get_bead` は正本のみ(タグを含まない)。`retrieve` /
  `get_bead_with_projection` は投影(tags / links / status)を manifest(run_id)付きで返す
- 語彙: `antigens` 引数 → `tags`、`search_antigens` → `search_tags`、
  `get_siblings`・`get_sibling_links` → `get_links`(relation フィルタ。restricted 参照 drop の
  漏洩対策を継承)、`include_siblings` → `include_links`
- retrieve 既定: retracted 除外 / amended は最新版に置換(旧版は明示フラグ)/
  **unattested 除外(明示フラグ時のみ `not_for_clinical_action` ラベル付き)**
- create_bead: clinical_note / assessment / attestation / retraction を受理(role ゲート維持)。
  amends / retracts 指定可。**antigens は受け取らない**(投影の仕事)

## 6. 実装分割(6ユニット、各ユニットで reviewer 検証 + checkpoint commit)

| U | 内容 | 備考 |
|---|---|---|
| U1 | ハッシュ規約: bead.go(antigens 撤去、amends/retracts 追加)+ 全パッケージの追随(タグ抽出を IndexBead 時へ移動)+ ゴールデンハッシュ再生成 | 全基盤。repo は常に green を維持 |
| U2 | 投影スキーマ: マイグレーション(bead_tags / clinical_links / bead_status / active_* / projection_manifest)+ 辞書/ルールの Bead 化 | **Codex peer 必須**(破壊的) |
| U3 | link projector(apc 書き換え、暴走防止の仕分け) | U2 後 |
| U4 | 状態導出(bead_status / active_views、チェーン解決規則、クリアランス継承) | U3 と並行可 |
| U5 | API 語彙(MCP/REST 改名、get_links 統合、retrieve 既定挙動) | UI 契約への影響は REST 投影の変更として別途 ts-client-builder |
| U6 | 再 ingest(clinical_note 取り込み = Synthea DocumentReference 復活)+ bench の実験軸再定義(dag_full/nosib = リンク投影の有無) | 再 ingest ~3.5h、**ビルドタグ sqlite_fts5 を runbook 明記** |

## 7. 死文化リスト(v2.1 → v3.1 で廃止)

daily_summary(グルーピングノード)/ actor: 名前空間 / EMR-CSV 抽出規則 /
sibling_link Bead 型と旧 relation 10種・severity 4段固定 / 二次応答と generation 減衰 /
bead_apc_scan.scan_generation / sibling_pairs.sibling_link_id / `bead_edges.edge_type='sibling'` /
MCP search_antigens・get_siblings・get_sibling_links・include_siblings / Bead.antigens フィールド /
DESIGN_v3 §8 の sibling 系 tier 定義。SPECIFICATION v3 の起草(全面改訂)は実装完了後。
