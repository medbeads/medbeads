# U6 clinical_note 取り込み + bench 軸再定義 + 再 ingest 仕様 — peer 統合確定版

ステータス: **確定(data-reviewer[Claude] + Codex peer の2独立レビューを統合、2026-07-11)**
親設計: DESIGN_v3.1_draft.md §"新しい Bead 型" + §6、docs/requirements.md N6/Q1。両ピア判定はいずれも
**条件付き GO**。統合ルーブリック: 合意点採用、data-reviewer の2発見を実証で確認、crux 1件を実証で決着。

## 合意点(両ピア一致 → 採用)

1. **base64→raw_text デコード**。生 base64 を content に**絶対入れない**(collectStrings が FTS を汚染)。
2. **clinical_note 専用 flattener を U6 で入れる**。DefaultFlattener 内で `b.Type=="clinical_note"` を先に
   分岐(別 flattener だと Engine.Open/Reindex の配線漏れ)。raw_text を**順序保存**で search_text に、
   summary は raw_text 先頭の非空行/最初の見出し行を数十字。他 FHIR 型の flattener は U6 スコープ外。
3. **本文 NLP tags 抽出は NO-GO**(決定論・二層分離に反する。辞書/モデル版を固定できない)。untagged 既定。
   本文由来 tags は将来 deterministic note tagger として別ユニット。
4. **parent エッジ**: nested `context.encounter[0].reference` を親 Encounter に(実測 983/983=100% 解決、
   1 Encounter=1 docref)。既存 resource_encounter_reference を拡張(top-level 優先、無ければ nested)。
   fallback は patient root だが **silent でなく ingest stats に count + warning**。encounter[] 長さ>1 は警告。
5. **sections[] の S/O/A/P 分割は U6 では後回し**(Synthea 見出しは SOAP と1対1でない。正本に解釈を焼き込まない。
   raw_text 保持済みなので後日投影側で導出可能)。U6 は raw_text 無加工保持のみ、sections[] 未設定。
6. **U6a(Python + flattener + bench、小規模検証)/ U6b(実ストア再 ingest ~3.5h、リード実行)に分割**。

## 実証で確認した data-reviewer の2発見(codex 未指摘、実データ裏取り)

### 発見1【最重要・GO/NO-GO 分岐】: superseded ノートの氾濫 → **status=="current" のみ ingest**
- 実証: サンプルバンドルで DocumentReference status = **superseded 18 / current 1**(95%)。data-reviewer の
  全数集計では 953/983 = 97% superseded。Synthea は受診ごとに「その時点までの累積ノート」を新規発行し過去を
  superseded 化。無条件全件だと患者あたり ~33 ノート(最大118)、1,135患者で ~37K の near-duplicate Bead →
  FTS/retrieve/judge FULL_RECORD/ground-truth attribution を破壊。
- **採用: clinical_note に限り `status=="current"` のみ ingest**(fhir.clinical_resources に条件追加)。
  最新の累積ノートのみ = 患者あたり数件に激減。過去ナラティブ保持が要るなら amends チェーン化を別ユニットへ。
  → **ただし「過去ナラティブを捨てる」判断はユーザー裁定を仰ぐ**(下記)。

### 発見2: type の LOINC coding が untagged 方針を裏切る → coding[] を content に残さない
- 実証: 実 docref の `type.coding` は LOINC(34117-2 "History and physical note" 等)を持つ。content に
  coding[] 構造を丸ごと入れると antigen.Extract が `loinc:34117-2`(文書種別)を誤タグ化 → Q3 の untagged と矛盾。
- **採用: content の source メタは `raw_text / source_system / source_document_id / language / status /
  note_type_code(LOINC を保持したいなら coding[] でなく文字列フィールド)` に限定。coding[] 構造は残さない。**

## 実証で決着した crux: bench 軸 → **dag_full/dag_nosib を単一 dag に統合**

- 相違: codex「dag_links_off/on に再定義」/ data-reviewer「include_links は Items 不変 → 2アーム統合」。
- 実証: `TestRetrieve_IncludeLinksFalse_LeavesContextBundleUnaffected`(retrieve_test.go:227)が
  「include_links=false でも Items 長さが default と同一」を保証。include_links は clinical_links sidecar の
  有無だけをゲートし、Items(LLM に渡る文脈)に無影響 → include_links で dag_full/dag_nosib を区別しても
  **同一の retrieval_score・token_usage を二度測るだけの無意味な重複**。
- **決着: data-reviewer 案採用。dag_full/dag_nosib を単一 `dag` に統合**(sibling 概念が U5a で消えた以上、
  区別が無い)。4アーム = rag / fts / dag / (任意 dag_links)。dag_links を残すなら clinical_links を answer
  プロンプトに注入する経路(RetrievalResult→answer)まで実装(U6 スコープ超のため任意)。論文 Limitations に
  「近傍展開廃止(U5a)に伴い dag は単一アーム、clinical_links は sidecar として別途評価」と明記。

## bench/ Python 旧語彙追随(U5 follow-up、必須)
- mcp_client.py: `search_antigens`→`search_tags`(:163)/ arg `antigen`→`tag`(:160)/ `antigens`→`tags`(:206)/
  `include_siblings`→`include_links`(:202)。**`apc_trigger`(:254)は削除**(U5a で Go 側除去済み、現在エラー)。
- 再投影は CLI `medbeadsd reproject -data <dir>` を runbook に明記(MCP ツール化は別スコープ)。
- テスト追随: bench/tests/ingest/test_integration.py(search_antigens)、retrieval/test_4arm・test_dag。

## U6b 再 ingest runbook(リード実行、実ストア保護)
1. **ビルド**: `CGO_ENABLED=1 go build -tags sqlite_fts5 -o medbeadsd ./cmd/medbeadsd`(sqlite_fts5 タグ必須)。
2. **ingest**: `uv run python -m bench.ingest --fhir-dir ~/medbeads-synthea/output/fhir --data-dir <新dir>
   --medbeadsd ./medbeadsd`。**~/medbeads-synthea/medbeads_data は絶対に上書きしない(新 dir)**。
3. **投影**: `./medbeadsd reproject -data <新dir>`(clinical_links/bead_status)。apc_trigger ではない。
4. **冪等確認**: content-address なので再実行で Bead 集合不変を1患者で確認。
5. **manifest/scenario 再生成**: clinical_note 追加で dataset_fingerprint が変わる → 既存 scenario の
   evidence_bead_ids が旧 ID を指すなら再マッピング/再生成(U6b の一部)。
6. 完了後: 性能再実測 → 論文数値(0710_manuscript_v3/notes/)を最終化。

## must-fix 3点(GO 条件)
1. superseded 制御(clinical_note は status=="current" のみ)— GO/NO-GO 分岐。
2. type の LOINC coding を content から除く(untagged を実成立)+ ClinicalNoteFlattener。
3. bench 軸を機械置換しない(dag 統合、apc_trigger→reproject CLI)。

## ユーザー裁定を仰ぐ点
- **superseded ノートの扱い**: (A) status=="current" のみ ingest(最新累積のみ、過去ナラティブ破棄)/
  (B) 全件 ingest し retrieve/投影側で superseded を除外(不変事実は全保持、Synthea の累積再発行を amends
  チェーン化する追加設計が要る)。両ピア推奨は (A)。臨床的に「過去時点で医師が何を書いていたか」の追跡
  (UC4 インシデント振り返り)が要るなら (B) だが U6 スコープ超。
