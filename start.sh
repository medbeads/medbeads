#!/bin/bash
# MedBeads - 全サーバー起動スクリプト
# Usage: ./start.sh

set -e

# プロジェクトルートディレクトリ
SCRIPT_DIR="$(cd "$(dirname "$0")" && pwd)"
cd "$SCRIPT_DIR"

echo "🚀 MedBeads サーバー起動中..."
echo ""

# 既存のプロセスを停止
echo "🧹 既存プロセスのクリーンアップ..."
lsof -t -i :8080 | xargs kill -9 2>/dev/null || true
lsof -t -i :8000 | xargs kill -9 2>/dev/null || true
lsof -t -i :5174 | xargs kill -9 2>/dev/null || true
sleep 1

# 1. Core Server (Go) - Port 8080
echo "📦 [1/3] Core Server (Go) 起動中... (Port 8080)"
cd "$SCRIPT_DIR/core"
go run main.go &
CORE_PID=$!

# Wait for Core to start
echo "⏳ Core Serverの起動を待機中..."
sleep 5

# 1.5 Data Ingestion (Optional Check)
# オブジェクトディレクトリが空（または少ない）場合のみデータを投入する簡易ロジック
# 必要に応じて --limit 数を調整するか、毎回実行するか決定してください
DATA_COUNT=$(ls "$SCRIPT_DIR/core/medbeads_data/objects" 2>/dev/null | wc -l)
if [ "$DATA_COUNT" -lt 5 ]; then
    echo "📥 [初期化] FHIRデータの取り込みを実行中 (Sample 5件)..."
    uv run --with requests "$SCRIPT_DIR/scripts/mass_ingest.py" "$SCRIPT_DIR/FHIR_sample" --limit 5 || echo "⚠️ データ取り込みに失敗しました（起動は続行します）"
else
    echo "ℹ️ データは既に存在します (Count: $DATA_COUNT). スキップします."
fi

# 2. AI API Server (Python/FastAPI) - Port 8000
echo "🤖 [2/3] AI API Server (Python) 起動中... (Port 8000)"
uv --directory "$SCRIPT_DIR/api" run uvicorn main:app --host 0.0.0.0 --port 8000 &
API_PID=$!
sleep 2

# 3. UI Server (Vite/React) - Port 5174
echo "🎨 [3/3] UI Server (Vite) 起動中... (Port 5174)"
cd "$SCRIPT_DIR/ui"
npm run dev &
UI_PID=$!

echo ""
echo "✅ 全サーバー起動完了!"
echo ""
echo "┌────────────────────────────────────────────────────┐"
echo "│ MedBeads サービス一覧                              │"
echo "├────────────────────────────────────────────────────┤"
echo "│ 🌐 UI:          http://localhost:5174             │"
echo "│ 📦 Core API:    http://localhost:8080             │"
echo "│ 🤖 AI API:      http://localhost:8000             │"
echo "└────────────────────────────────────────────────────┘"
echo ""
echo "停止するには Ctrl+C を押してください"
echo ""

# シグナルハンドリング（Ctrl+C で全プロセス停止）
trap "echo ''; echo '🛑 サーバーを停止中...'; kill $CORE_PID $API_PID $UI_PID 2>/dev/null; exit 0" SIGINT SIGTERM

# プロセスを維持
wait
