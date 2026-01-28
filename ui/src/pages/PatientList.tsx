import { useEffect, useState } from 'react';
import { useNavigate } from 'react-router-dom';
import { fetchAllPatients, type Bead } from '../lib/api';
import { Users, Search, Activity, Calendar } from 'lucide-react';

export default function PatientList() {
  const [patients, setPatients] = useState<Bead[]>([]);
  const [loading, setLoading] = useState(true);
  const [query, setQuery] = useState('');
  const navigate = useNavigate();

  useEffect(() => {
    loadPatients();
  }, []);

  const loadPatients = async () => {
    try {
      const data = await fetchAllPatients();
      setPatients(data || []);
    } catch (e) {
      console.error(e);
      alert("Failed to load patients");
    } finally {
      setLoading(false);
    }
  };

  const filtered = patients.filter(p => {
    const q = query.toLowerCase();
    const name = (p.content?.name as string || '').toLowerCase();
    const id = p.id.toLowerCase();
    return name.includes(q) || id.includes(q);
  });

  return (
    <div className="min-h-screen bg-[hsl(var(--color-bg-h),var(--color-bg-s),var(--color-bg-l))] text-[hsl(var(--color-text-h),var(--color-text-s),var(--color-text-l))] p-8">
      <header className="mb-12 text-center max-w-2xl mx-auto">
        <div className="flex justify-center mb-6">
            <div className="p-4 rounded-full bg-white/5 border border-white/10 shadow-[0_0_30px_rgba(255,255,255,0.05)]">
                <Activity size={48} className="text-[hsl(var(--color-primary-h),var(--color-primary-s),var(--color-primary-l))]" />
            </div>
        </div>
        <h1 className="text-4xl font-bold mb-4 bg-clip-text text-transparent bg-gradient-to-r from-blue-400 to-purple-400">
          MedBeads Context EMR
        </h1>
        <p className="text-lg opacity-60 leading-relaxed">
          AI-Powered Context Awareness System for Medical Records. <br/>
          Select a patient to visualize their timeline, graph, and insights.
        </p>
      </header>

      <div className="max-w-4xl mx-auto">
        {/* Search Bar */}
        <div className="relative mb-8 group">
          <div className="absolute inset-y-0 left-4 flex items-center pointer-events-none opacity-50 group-focus-within:opacity-100 group-focus-within:text-[hsl(var(--color-primary-h),var(--color-primary-s),var(--color-primary-l))] transition-all">
            <Search size={24} />
          </div>
          <input 
            type="text"
            placeholder="Search patients by name or ID..."
            value={query}
            onChange={(e) => setQuery(e.target.value)}
            className="w-full bg-black/20 border border-[var(--glass-border)] rounded-2xl py-4 pl-14 pr-4 text-xl focus:outline-none focus:border-[hsl(var(--color-primary-h),var(--color-primary-s),var(--color-primary-l))] transition-all shadow-lg backdrop-blur-sm"
          />
        </div>

        {/* List */}
        {loading ? (
            <div className="text-center py-20 opacity-50 animate-pulse">Loading Patient Directory...</div>
        ) : (
            <div className="grid grid-cols-1 md:grid-cols-2 gap-4">
                {filtered.map(patient => (
                    <button
                        key={patient.id}
                        onClick={() => navigate(`/patient/${patient.id}`)}
                        className="flex items-center gap-4 p-6 rounded-xl border border-[var(--glass-border)] bg-white/5 hover:bg-white/10 hover:border-[hsl(var(--color-primary-h),var(--color-primary-s),var(--color-primary-l))] transition-all text-left group"
                    >
                        <div className="w-12 h-12 rounded-full bg-gradient-to-br from-blue-500 to-purple-600 flex items-center justify-center shrink-0 shadow-lg group-hover:scale-110 transition-transform">
                            <Users size={24} className="text-white" />
                        </div>
                        <div className="flex-1 min-w-0">
                            <h3 className="text-lg font-bold truncate group-hover:text-[hsl(var(--color-primary-h),var(--color-primary-s),var(--color-primary-l))] transition-colors">
                                {patient.content?.name || "Unknown"}
                            </h3>
                            <div className="flex items-center gap-4 text-sm opacity-60 mt-1 font-mono">
                                <span>{patient.content?.gender || "Unknown"}</span>
                                <span className="flex items-center gap-1">
                                    <Calendar size={12} />
                                    {patient.timestamp.split('T')[0]} (DOB)
                                </span>
                            </div>
                            <div className="text-xs opacity-30 mt-2 truncate font-mono">ID: {patient.id}</div>
                        </div>
                    </button>
                ))}
            </div>
        )}
      </div>
    </div>
  );
}
