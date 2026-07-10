# スキーム批判レビュー 視点A: 内部整合性(data-reviewer)

2026-07-10 実施。実ストア(1,135患者・1,042,456 Bead・sibling_link 82,013・
sibling_pairs 862,945・distinct antigens 1,435)を読み取り専用で実測した上での批判。

## 1. L1「1行要約(summary)」が実質ゴミ — トークン効率の主張の土台が空洞【致命的】

根拠: index/flatten.go(DefaultFlattener)、全配線が DefaultFlattener のまま。
DESIGN §5・R3.3 は「fhir_medicationrequest →『メロペネム 1g 点滴静注 8時間毎』」という
種別ごとの人間可読平坦化を約束するが、FHIR-aware flattener は存在しない。実ストアの実際の
summary は `fhir_medicationrequest: 1535362`(内部ID)等 — content 文字列をアルファベット順
ソートした先頭1件で、臨床的意味がほぼ載らない。retrieve の L1(40tok)・切り捨て時の L2 参照が
全部これに依存。改善方向: 種別ごとの FHIR flattener を必須に格上げ。

## 2. sibling_link 82,013本の圧倒的多数が臨床的に無価値 — LOINC 機械共起が主成分【致命的】

実測: sibling_pairs 862,945行の matched_antigen 内訳 = loinc: 753,993(87%)/
snomed: 51,809 / temporal: 37,780 / organ: 9,086 / rxnorm: 4,743 / atc: 4,644 /
**risk: わずか890(0.1%)**。87% は「同じ検査パネルの観測値どうし」という自明な共起。
仕様の看板(薬物相互作用・禁忌)は 0.1%。1患者平均761ペアはノイズの洪水。
改善方向: LOINC/temporal をトリガーから除外し、risk:/atc:/rxnorm: の臨床イベント間に絞る。

## 3. 全 sibling_link が relation=clinical_correlation / severity=warning 固定【重大】

根拠: apc/link.go(ハードコード)。SIBLING_SPEC §4.3 の relation 10種・§4.4 の severity
4段階が実装では1値に潰れている。「ワーファリン↔NSAIDs=出血 alert」という中核デモが
実データで表現不能。改善方向: 相互作用テーブルから relation/severity を導出。

## 4. 誤記の訂正・削除・「忘れられる権利」への回答が皆無【致命的(実用カルテ目標に対して)】

grep で specs/コード全域に supersedes/amend/redact/tombstone がゼロ。spec §2 は
「修正は新しい Bead で表現」と言うが、新旧を結ぶエッジも「撤回済み」を検索側に伝える機構も
無い。誤入力バイタルを無効化できず retrieve は誤記を正データとして返し続ける。
改善方向: supersedes エッジ + 検索時フィルタ + tombstone 設計。

## 5. DAG が実質2階層フラット — spec §7.1 の Edge Rule が死に、implicit sibling 意味論が崩壊【重大】

実測: daily_summary Bead 0件。patient_registration の直接子は平均54.9・最大871。
「暗黙的 sibling = 同じ親の子」は「患者の全記録が相互に sibling」を意味し無意味。
改善方向: encounter を中間親にする(実装済み)か、仕様を現実に合わせて改訂。

## 6. antigen taxonomy が仕様と実装で乖離、静的辞書10件は非現実的【重大】

仕様は namespace 8種(actor: 含む)、実装は snomed/loinc/rxnorm 直接 + 辞書10件のみ。
actor: は完全未実装。実ストアで risk 抗原は 2,754行/192万行。「生成AIのカルテ」の横串知識が
薬剤10種の手書き表に依存。改善方向: RxNorm→ATC 公開クロスウォークで辞書を数千件に拡張、
未実装 namespace を仕様に明記。

## 7. スコア閾値の恣意性 — 4点/減衰0.5/頻度30%に臨床的根拠なし【中】

実データで大量リンクが score 5.00 に張り付き、ランキングとして機能していない。
改善方向: スコアは gate に用途限定し、順位付けは別シグナルで。

## 8. Bead 単位 = FHIR リソース丸ごとが retrieve 単位として粗い/細かい両極【中】

実測: fhir_observation が504,813件(48%)で単一観測値の断片。**antigen を持つ Bead は
17,561件のみ = 98.3% の Bead が antigen 0 で sibling/antigen 検索の網に載らない**。
改善方向: encounter 単位の集約ビュー、antigen 抽出の異常フラグへの拡張。

## 9. single-writer + SetMaxOpenConns(1) が実運用の同時性と衝突【中】

複数エージェントの同時 retrieve が互いをブロックする。ローカルファースト単独では十分だが
実用像とは不整合。改善方向: 読み取り専用接続プールの分離。

## 10. retrieve の provenance が「score固定・relation固定」で監査価値が薄い【軽微】

項目1–3の是正で自動的に改善する従属項目。

## 守る価値のある強み

1. **内容ハッシュ=ID + JCS(RFC8785)正準化の厳密さ** — 決定論的 ID = 研究再現性と
   tamper-evidence の土台。唯一無二の差別化。絶対に薄めない
2. **「正本(Pod)が常に先、index は完全再構築可能」の一方向依存** — 劣化項目を
   作り直しで一括修正できる回復力
3. **決定論的 sibling_link timestamp = max(親timestamp)** — 再スキャンの冪等性

## 結論

項目1(summary 未実装)・項目2/3(sibling 意味論の崩壊)・項目4(訂正/削除不在)は
「実装済み」ステータスと実体の乖離が大きく、要件 R3.3・R5 は実装済み判定を差し戻すべき。
