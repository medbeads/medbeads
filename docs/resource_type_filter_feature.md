# FHIRリソースタイプフィルター機能

## 概要
検索画面にFHIRリソースタイプのチェックボックスフィルター機能を追加しました。この機能により、特定のFHIRリソースタイプを含む患者のみを検索結果に表示できます。

## 実装内容

### バックエンド変更 (Go)

#### 1. `medbeads/core/store/store.go`
- **新関数追加**:
  - `SearchPatientsByContentWithResourceTypes`: リソースタイプフィルタリングに対応した検索関数
  - `searchByResourceTypes`: リソースタイプのみで検索
  - `searchWithTypeFilter`: テキスト検索とリソースタイプフィルタリングの組み合わせ
  - `LoadFromCAS`: CASからBeadオブジェクトをロードする関数

#### 2. `medbeads/core/api/server.go`
- **handleSearch関数を拡張**:
  - `resourceTypes`パラメータを受け入れ
  - カンマ区切りのリソースタイプリストをサポート
  - 空のクエリでもリソースタイプフィルタリングのみで検索可能

### フロントエンド変更 (React/TypeScript)

#### 1. `medbeads/ui/src/lib/api.ts`
- **searchPatients関数を拡張**:
  - オプショナルな`resourceTypes`パラメータを追加
  - APIリクエストにリソースタイプパラメータを含める

#### 2. `medbeads/ui/src/components/PatientSidebar.tsx`
- **新UI要素追加**:
  - "Filter by Resource Type"トグルボタン
  - 8種類のFHIRリソースタイプのチェックボックス
  - 選択されたフィルター数の表示
  - "Clear all filters"ボタン

- **新機能追加**:
  - `selectedResourceTypes`ステート管理
  - `toggleResourceType`関数でフィルターの追加/削除
  - リソースタイプフィルタリングとテキスト検索の連携

## フィルタリング可能なリソースタイプ

| リソースタイプ | 表示名 | データ件数 |
|:------------|:------|:----------|
| encounter | Encounter | 41 |
| medication | Medication | 28 |
| observation | Observation | 241 |
| condition | Condition | 45 |
| diagnostic_report | Report | 122 |
| procedure | Procedure | 133 |
| immunization | Immunization | 13 |
| imaging_study | Imaging | 4 |

## 使い方

1. **フィルターを開く**:
   - 検索バー下の"Filter by Resource Type"ボタンをクリック

2. **リソースタイプを選択**:
   - 表示したいリソースタイプのチェックボックスを選択
   - 複数選択可能（OR条件で検索）

3. **検索実行**:
   - フィルター選択後、自動的に検索が実行される
   - テキスト検索と組み合わせることも可能

4. **フィルターをクリア**:
   - "Clear all filters"ボタンをクリックして全フィルターを解除

## テスト方法

```bash
# 1. Goサーバーを起動
cd medbeads/core
./medbeads

# 2. UIを起動（別ターミナル）
cd medbeads/ui
npm run dev

# 3. ブラウザで http://localhost:5173 にアクセス
# 4. 右側のサイドバーで検索フィルターをテスト
```

## 今後の拡張可能性

- フィルター条件の保存機能
- AND/OR条件の切り替え
- リソースタイプごとのカウント表示のリアルタイム更新
- より詳細なサブフィルター（例：Observationの中でも特定のタイプのみ）