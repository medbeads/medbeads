1. **標準医療情報モデルをBead型で再発明している**
重要度: 致命的  
根拠: `specs/MEDBEADS_SPECIFICATION_v2.1.md` §5, §19.1-19.2 / `specs/DESIGN_v3.md` §4  
何が問題か: FHIR Resourceを `fhir_*` Beadへ薄く写す一方、openEHR archetypeのような臨床概念単位の制約、FHIR profile、terminology binding、CDA narrative相当の「人間が読める法的本文」がない。結果として「保存形式」はあるが「カルテ意味論」がない。  
改善方向: Beadはイベント格納単位に限定し、FHIR profile/openEHR archetype/SNOMED bindingを検証層として第一級化する。

2. **自由記述メモへの対抗軸が弱い**
重要度: 致命的  
根拠: `internal/engine/index/flatten.go:18-41`, `specs/DESIGN_v3.md` §8  
何が問題か: 検索用 summary は型別臨床レンダリングではなく、現状は content 内文字列を集める汎用 fallback。医師メモの強みである narrative、推論過程、不確実性、鑑別、方針のニュアンスを保存・提示するモデルがない。  
改善方向: CDA/openEHR composition 的な narrative Bead または `clinical_note` archetype を導入し、構造化データと双方向リンクする。

3. **antigen/APC は臨床知識グラフではなくタグ共起エンジン**
重要度: 致命的  
根拠: `specs/MEDBEADS_SPECIFICATION_v2.1.md` §9.3 / `internal/engine/apc/score.go:27-85`  
何が問題か: 共通 antigen、時間近接、型ボーナスの加点で sibling_link を作るだけで、SNOMED CT の subsumption、LOINC axis、RxNorm/ATC の階層、薬剤相互作用知識、否定・状態・重症度を扱わない。臨床推論に見えるが実体は共起リンク。  
改善方向: APC を汎用共起から切り離し、ルール/terminology service/薬剤知識ベース由来の typed relation 生成器に分解する。

4. **sibling_link の relation/severity が仕様と実装で破綻している**
重要度: 致命的  
根拠: `specs/MEDBEADS_SPECIFICATION_v2.1.md` §8.4-8.5 / `internal/engine/apc/link.go:22-33`, `internal/engine/apc/link.go:90-95`  
何が問題か: 仕様は drug_interaction, contraindication, causal 等を掲げるが、実装は全リンクを `clinical_correlation` かつ `warning` に固定している。薬剤相互作用や禁忌を自動生成するという主張と実装能力が一致しない。  
改善方向: relation ごとに独立した evidence model、必須入力、検証ルール、confidence を定義する。

5. **免疫系メタファーの概念コストが高すぎる**
重要度: 重大  
根拠: `specs/MEDBEADS_SIBLING_SPEC.md` §1.2, §6 / `specs/MEDBEADS_SPECIFICATION_v2.1.md` §6, §9  
何が問題か: antigen/APC/二次応答という比喩が、医療情報学で既にある code, terminology binding, inference rule, relationship, provenance を別名で包んでいる。比喩のために設計判断が曖昧になり、標準用語との対応も不明瞭。  
改善方向: 外部APIでは「code/tag/rule/link/provenance」に戻し、免疫系語彙は内部ニックネーム以下に落とす。

6. **内容ハッシュIDに antigens を含める設計が知識改訂に弱い**
重要度: 重大  
根拠: `specs/MEDBEADS_SPECIFICATION_v2.1.md` §6.2, §12.1 / `internal/engine/bead/bead.go:41-72`  
何が問題か: antigen は医学知識・辞書・マッピング更新で変わる派生情報なのに、ID対象に含まれる。辞書改訂だけで臨床事実のIDが変わり、event sourcing の「事実イベント」と CQRS の「読みモデル」を混同している。  
改善方向: 原事実BeadのIDから派生タグを外し、antigen assignment を別Beadまたは再構築可能な projection にする。

7. **CQRSの読みモデル更新がイベント履歴として設計されていない**
重要度: 重大  
根拠: `specs/DESIGN_v3.md` §5, §7 / `internal/engine/apc/scanner.go:95-140`, `internal/engine/index/write.go:87-207`  
何が問題か: index は再構築可能キャッシュと言いつつ、APC scan state、sibling_pairs、embedding queue など処理状態がDBに混在する。どの設定・辞書・コード版で projection が作られたかがイベントとして残らない。  
改善方向: projection manifest `{code version, dictionary hash, config hash}` と再計算イベントを保存し、読みモデルを明示的に世代管理する。

8. **FHIRの「参照・状態・履歴」の思想を十分に引き継いでいない**
重要度: 重大  
根拠: `specs/MEDBEADS_SPECIFICATION_v2.1.md` §19.1-19.2 / `docs/requirements.md` R4.4  
何が問題か: FHIR Reference、Encounter文脈、status、effective/issued/recorded の時刻差、provenance、modifierExtension の扱いがBead親子とtimestampへ単純化される。FHIRを入れているがFHIRの安全設計を捨てている。  
改善方向: FHIR Resourceは原形保存し、FHIRPath/Profile validation と reference graph をBead DAGとは別レイヤで保持する。

9. **patient_root 導出と shared Pod が医療グラフとして危うい**
重要度: 重大  
根拠: `specs/DESIGN_v3.md` §3 / `internal/engine/bead/bead.go:41-44`  
何が問題か: patient_root はハッシュ対象外の導出メタデータで、parentsなし/複数rootは `_shared` へ落ちる。多患者にまたがる家族歴、感染、妊娠・新生児、臓器移植、施設マスターなどの責任境界が曖昧になる。  
改善方向: subject/encounter/care-context を明示フィールドまたは署名済みメタとして持ち、多主体イベントを一級概念にする。

10. **clearance がプライバシー設計として弱い**
重要度: 重大  
根拠: `specs/MEDBEADS_SPECIFICATION_v2.1.md` §18.1, §18.5 / `internal/engine/clearance/access.go:137-164`, `internal/mcpserver/render.go:103-135`  
何が問題か: 仕様は ghost record で存在を見せると明記し、実装もID/Type/Timestamp/Parentsを残す。精神科、HIV、妊娠、薬物検査では「存在」や時刻自体がPHIであり、横断リンクから推測漏洩する。  
改善方向: role別に「非存在化」「粗粒度化」「存在のみ」などの開示ポリシーを分け、リンクも同時に遮断する。

11. **緊急/system ロールの無条件バイパスが監査不十分**
重要度: 重大  
根拠: `specs/MEDBEADS_SPECIFICATION_v2.1.md` §18.1, §18.6 / `internal/engine/clearance/access.go:74-80`  
何が問題か: emergency/system は常に許可されるが、アクセス時の監査書き込みがアクセス関数に強制されていない。仕様の「必ず監査ログ」と実装境界が一致していない。  
改善方向: access check と audit append を不可分なAPIにし、read path全体で監査を強制する。

12. **APCの二次応答は価値より暴走・ノイズのリスクが大きい**
重要度: 重大  
根拠: `specs/MEDBEADS_SPECIFICATION_v2.1.md` §8.7, §9.4 / `internal/engine/apc/scanner.go:116-133`, `internal/engine/apc/scanner.go:320-370`  
何が問題か: sibling_link 自体に antigen を持たせ、リンクのリンクを許す設計は説明困難で、臨床知識というよりグラフ密度を増やす。IDFや上限で抑えている時点で、概念が自然という主張は弱い。  
改善方向: 二次応答は既定無効にし、明示的な rule chain か clinical pathway に限定する。

13. **リンクの根拠が監査可能というには薄い**
重要度: 重大  
根拠: `specs/MEDBEADS_SPECIFICATION_v2.1.md` §20.1 / `internal/engine/apc/link.go:79-98`  
何が問題か: sibling_link の description は「何個の共有antigenでscoreいくつ」という生成文で、使用した辞書版、スコア内訳、除外候補、閾値、ルールID、臨床知識ソースを持たない。再現性と説明責任が不足。  
改善方向: link content に rule_id、rule_version、score breakdown、source terminology、negative evidence を入れる。

14. **MCP retrieve が臨床質問応答APIとして不透明**
重要度: 重大  
根拠: `specs/DESIGN_v3.md` §8 / `internal/mcpserver/retrieve.go:115-229`  
何が問題か: 複数患者にヒットした場合は最初の patient_root に寄せ、他患者anchorを黙って捨てる。トークン予算の貪欲詰め込みも臨床重要度ではなくtier順で、落ちた理由が弱い。  
改善方向: retrieve は query planning 結果、除外理由、患者曖昧性、臨床優先度スコアを返す。

15. **FTS/semantic の要約品質が臨床安全に直結するのに未設計**
重要度: 中  
根拠: `docs/requirements.md` R3.3, R6.2 / `internal/engine/index/flatten.go:18-41`  
何が問題か: L1 summary はagentの主要入力なのに、生成責任、検証、更新、原文対応、数値単位保持、否定表現保持がない。要約が間違えばAIの文脈が壊れる。  
改善方向: summary を検証可能な派生Beadにし、原文span/JSONPathと生成器バージョンを持たせる。

16. **イベントソーシングとして correction/amendment が未成熟**
重要度: 中  
根拠: `specs/MEDBEADS_SPECIFICATION_v2.1.md` §2, §7 / `internal/engine/bead/bead.go:45-58`  
何が問題か: 「修正は新Bead」と言うが、誤記訂正、取り消し、entered-in-error、supersedes、attests、legal amendment の型が体系化されていない。医療記録では不変性だけでは足りない。  
改善方向: `amends/retracts/supersedes/attests` を標準edge typeとして定義し、FHIR Provenance/AuditEventと対応させる。

17. **臨床時間モデルが単一timestampに潰れている**
重要度: 中  
根拠: `specs/MEDBEADS_SPECIFICATION_v2.1.md` §4.1-4.2, §19.2 / `internal/engine/apc/link.go:44-70`  
何が問題か: 医療データには発生時刻、記録時刻、発行時刻、有効期間、検査採取時刻、結果確定時刻がある。単一 timestamp と max(timestamp) の sibling 時刻では因果・監査・検索が歪む。  
改善方向: event_time/recorded_time/available_time/valid_period を分離する。

18. **スコープ外に実用上の核心を送りすぎている**
重要度: 中  
根拠: `docs/requirements.md` §8 / `specs/MEDBEADS_SPECIFICATION_v2.1.md` §17  
何が問題か: 処方チェック、DID署名、EMR-CSV、APCイベント駆動、1万患者、多施設、clearance UI監査がM4以降。目標が実用カルテ基盤なら、v3.0は研究用retrieval基盤に近い。  
改善方向: 「研究v3」と「臨床実用v3」を分け、臨床安全に必要な最小セットを前倒しする。

**このスキームで最も守る価値のある強み**

1. 内容ハッシュID、不変Bead、append-only Pod、reindex可能なindexという「正本と投影の分離」は強い。  
2. MCPを第一級APIにして、生成AIの取得単位・監査単位を明示しようとしている点は正しい。  
3. 患者単位Podでローカルファーストに高速な文脈取得を狙う設計は、RAG基盤として実用的な核がある。