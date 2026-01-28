import { User } from 'lucide-react';

export default function Home() {
  return (
    <div className="h-full flex items-center justify-center bg-slate-50 p-6">
      <div className="text-center text-slate-500 max-w-md">
        <div className="w-24 h-24 mx-auto mb-6 bg-white rounded-full flex items-center justify-center shadow-sm border border-slate-200">
             <User className="w-12 h-12 text-slate-300" />
        </div>
        <h2 className="text-2xl font-bold text-slate-700 mb-2">患者を選択してください</h2>
        <p className="text-lg text-slate-500 leading-relaxed">
          右側の患者一覧から患者を選択すると、<br/>
          電子カルテ（時系列・詳細）が表示されます。
        </p>
      </div>
    </div>
  );
}
