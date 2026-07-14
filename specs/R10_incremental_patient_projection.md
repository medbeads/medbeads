# R10 患者単位の自動増分投影

日付: 2026-07-14

## 目的

MedBeads は、患者データが継続的かつ大量に追記されることを前提とする。通常のBead追記で全患者の
RAG/Graph構造を再構築してはならない。一方、索引だけが新しく clinical_links や
bead_status が古い中間状態を読取り側に見せることも許容しない。

## 採用した整合性境界

患者Beadの通常追記は次の順序で処理する。

1. 正本である患者Podへフレームをappendし、fsyncする。
2. SQLiteの単一トランザクション内で以下を行う。
   - IndexBead（beads / edges / tags / FTS / Pod watermark）
   - 当該患者だけの clinical_links 再計算
   - 当該患者の bead_status / active_conditions / active_medications 更新
   - patient_projection_state watermark更新
3. まとめてcommitする。

したがってcommit前の読取りは旧索引+旧投影、commit後は新索引+新投影を観測し、
「Beadだけ存在してリンク・状態が未更新」というSQLite上の中間状態は存在しない。

Pod append後、SQLite commit前に停止した場合は、次回 OpenWithOptions(AutoProject=true) が
CatchUp で索引を進める。その後 pods.indexed_upto と patient_projection_state.indexed_upto
が不一致の患者だけを再投影し、読取りを受け付ける前に一致させる。

## なぜ clinical_links は患者全体を再計算するか

単純に「新しいBeadに接するリンクだけ」を追加すると誤る。cooccurrence projectorには患者内の
出現頻度閾値（30%）とBead当たりリンク上限があり、分母が1件増えただけで、過去Bead同士のリンクが
新たに適格化または不適格化し得る。このためリンクは患者単位で全置換する。ただし他患者・他Podは
一切走査しない。

record_stateは通常Bead、未承認amendmentでは新しい1行だけをO(1)追加する。過去状態を変え得る
attestation / retractionに限り、患者Pod全体の訂正チェーンを再解決する。

## 知識世代とデータwatermark

- projection_manifest: code versionとknowledge Bead IDsを固定する不変の「解釈世代」。
- patient_projection_state: その世代で各患者Podのどのoffsetまで投影済みかを示す再構築可能な現在表。
- clinical_links.projection_run_id / bead_status.projection_run_id: 使用した解釈世代を指す。

通常追記では同じ解釈世代を再利用する。組込みlink rule、curated rule、または投影コード版が変わった
場合は、新しいmanifestをローリング目標としてactiveにし、患者を優先度付きqueueへ登録する。全患者を
Open中に同期Reprojectしない。その後の追記はqueueを待たず当該患者だけを新世代へ移す。共有Podへ
新しいknowledge Beadを発行しただけでは勝手に有効化せず、明示的なmedbeadsd reprojectにより閉じた
knowledge集合を選ぶ。詳細はR11。

## 実行面

- medbeadsd serve と medbeadsd correct は自動投影を有効にする。
- engine.Open は低レベル保守・projectorテストとの互換のため手動モードを維持し、
  実運用は OpenWithOptions を使う。
- -projection-code-version の変更は優先度付きローリング再投影を開始する。
- 同一Engine内のIngestはappendからcommitまで直列化する。現在のSQLite接続プール自体が
  1 connectionなので、この制約は既存の書込みモデルと一致する。

## 残る性能課題

患者内リンク再計算は全患者再構築ではないが、1患者内では入力件数に応じて増える。通常想定の
患者Pod（約900 Bead）では境界内だが、将来、単一患者が極端に肥大化する場合は
「頻度閾値・capの変化点を保持する差分projector」を別ユニットで設計する。正確性を落とす
append-onlyリンク方式へは変更しない。
