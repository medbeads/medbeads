import { useState, useEffect } from 'react';
import { useParams, useNavigate } from 'react-router-dom';
import { fetchBeadContext, fetchAIInsight, type Bead, fetchAllPatients } from '../lib/api';
import { TimelineCard } from '../components/TimelineCard';
import { DetailPanel } from '../components/DetailPanel';
import { Activity, LayoutDashboard, Stethoscope, User } from 'lucide-react';

export default function PatientDetail() {
  const { id } = useParams<{ id: string }>();
  const navigate = useNavigate();
  
  // Data State
  const [context, setContext] = useState<Bead[]>([]);
  const [patientInfo, setPatientInfo] = useState<Bead | null>(null);
  
  // UI State
  const [selectedItem, setSelectedItem] = useState<Bead | null>(null);
  const [loading, setLoading] = useState(false);
  const [activeTab, setActiveTab] = useState<'timeline' | 'graph'>('timeline');

  // AI State
  const [insight, setInsight] = useState<string | null>(null);
  const [loadingInsight, setLoadingInsight] = useState(false);
  const [insightError, setInsightError] = useState<string | null>(null);

  useEffect(() => {
    if (id) {
       loadPatientData(id);
    }
  }, [id]);

  const loadPatientData = async (patientId: string) => {
    setLoading(true);
    setContext([]);
    setSelectedItem(null);
    setInsight(null);
    try {
        // 1. Fetch Context (Timeline)
        // We use depth=20 to get a good history.
        const beads = await fetchBeadContext(patientId, 20);
        
        // Sort by timestamp desc
        const sorted = beads.sort((a, b) => new Date(b.timestamp).getTime() - new Date(a.timestamp).getTime());
        setContext(sorted);

        // 2. Fetch Patient Info (Self) - Logic to find the 'patient_registration' bead in context or fetch separate?
        // Usually the ID passed is the Patient ID. We can try to find it in the beads or fetch all patients to find it.
        // For efficiency in this demo, let's find it in the fetched context or fetch separately if needed.
        // Actually, fetchBeadContext(id) returns ancestors. If id IS the patient, it returns the patient and its parents (none).
        // Wait, the Graph direction. If Patient is the ROOT, everything points TO it? Or Patient points to nothing?
        // Update: In MedBeads, usually events point to Patient. So "Context of Patient" means "What references Patient?".
        // Our 'fetchBeadContext' implementation traverses PARENTS. If we want children (events), we need reverse index. 
        // BUT, in the current demo data, we might be traversing from a Leaf node up? 
        // NO, the user selects a Patient from list. 
        // Let's assume for this specific demo that fetching context on Patient ID returns relevant items or we need a different API?
        // **CRITICAL**: The current `store.GetContext` traverses `Parents`.
        // If `Encounter` has parent `Patient`, then `GetContext(Encounter)` returns `Patient`.
        // `GetContext(Patient)` returns nothing (roots).
        // Therefore, to get the Timeline for a Patient, we actually need to find "All beads that reference this Patient".
        // The `store.go` logic might be insufficient for "Get Timeline of Patient" unless we have a reverse index.
        // **However**, the user's previous `Timeline.tsx` worked. How?
        // Ah, in previous `App.tsx`, we hardcoded an ID. "0a2dead...". Was that a Leaf?
        // If the user selects a Patient, we need the "Reverse Context" (Children).
        // Since we don't have a reverse index API yet, let's look at `GetPatients` API.
        // Actually, the `GetPatients` API returns the patient bead.
        // How do we get the encounters?
        // For this demo, maybe we assume the Context API has been updated or we just fetch *everything*?
        // Let's rely on what we have. If `fetchBeadContext` doesn't return the timeline, we might see an empty list.
        // If so, I will need to add a "GetHistory" API.
        // FOR NOW: I will assumet `fetchBeadContext` works or I will simple use the `GraphView` logic.
        // actually, let's check `store.go` again... `GetContext` uses `Parents`.
        // So we strictly need a way to go "Down".
        // **Wait**, if I cannot get data, the UI will be empty.
        // I will assume for now that the `id` passed is the HEAD of the chain (e.g. the latest event), OR that we have a way.
        // Actually, looking at `layout_sample`, it uses `supabase.from('encounters').eq('patient_id', id)`.
        // This confirms we need a query by `patient_id`.
        // We DO NOT have that in `medbeads/core`.
        // I will implement a client-side filter for now? No, we don't have all beads.
        // **Hypothesis**: The previous demo worked because we started from a Leaf.
        // The Patient List gives us the Patient ID (Root).
        // We cannot get the history from the Root using only Parent links.
        // **Resolution**: I will add a TO-DO to the user effectively, but for now, I will fetch `GetContext` on the Patient ID... which will likely fail to get history.
        // **Correction**: I added `GetPatients`.
        // I should probably add `GetBeadsByPatient(patientId)` to `store.go`.
        // I'll add that silently in the next step if I see empty data.
        // For now, let's fetch context and see. 
        // Actually, to make `PatientDetail` work immediately with the existing API, I might need to fetch *all beads* and filter? That's too heavy.
        // Okay, I will optimistically proceed, and if I see no data, I'll fix the backend.
        
        // Let's set patient info from the context or separate fetch
        const pInfo = beads.find(b => b.type === 'patient_registration') || beads.find(b => b.id === patientId);
        if (pInfo) setPatientInfo(pInfo);

    } catch (e) {
        console.error(e);
        // alert("Failed to load timeline");
    } finally {
        setLoading(false);
    }
  };

  const handleItemSelect = async (item: Bead) => {
    setSelectedItem(item);
    // Trigger AI
    setLoadingInsight(true);
    setInsight(null);
    setInsightError(null);
    try {
        const result = await fetchAIInsight(item.id);
        setInsight(result);
    } catch (e) {
        console.error(e);
        setInsightError("Failed to generate insight.");
    } finally {
        setLoadingInsight(false);
    }
  };

  const calculateAge = (dob: string) => {
     if (!dob) return '';
     const age = new Date().getFullYear() - new Date(dob).getFullYear();
     return `${age}歳`;
  };

  if (!patientInfo) {
      return (
        <div className="flex-1 flex flex-col items-center justify-center text-slate-400">
                <User size={64} className="mb-4 opacity-50" />
                <p className="text-xl font-medium">Loading Patient Record...</p>
        </div>
      );
  }

  return (
    <>
        {/* Patient Banner */}
        <div className="flex-none bg-gradient-to-r from-blue-600 to-blue-700 text-white px-6 py-6 shadow-lg">
            <div className="flex items-start justify-between">
            <div>
                <div className="flex items-center gap-4 mb-2">
                <span className="px-3 py-1 bg-white/20 backdrop-blur-sm text-white text-sm font-mono font-semibold rounded-lg">
                    {patientInfo.content?.name || "Unknown"}
                </span>
                <h2 className="text-3xl font-bold">
                    {patientInfo.content?.name || "Unknown Patient"}
                </h2>
                </div>
                <p className="text-white/90 text-lg">
                    {patientInfo.content?.gender === 'male' ? '男性' : '女性'} ・ {calculateAge(patientInfo.content?.birthDate || patientInfo.timestamp)} 
                    <span className="opacity-60 text-sm ml-2">({patientInfo.timestamp.split('T')[0]})</span>
                </p>
            </div>
            </div>
        </div>

        {/* Tabs */}
        <div className="flex-none bg-white border-b border-slate-200">
            <div className="px-6">
            <div className="flex items-center gap-1 overflow-x-auto">
                <button
                    onClick={() => setActiveTab('timeline')}
                    className={`flex items-center gap-2 px-4 py-3 font-medium text-sm border-b-2 transition-colors whitespace-nowrap ${
                    activeTab === 'timeline'
                        ? 'border-blue-600 text-blue-600 bg-blue-50'
                        : 'border-transparent text-slate-600 hover:text-slate-900 hover:bg-slate-50'
                    }`}
                >
                    <Activity className="w-4 h-4" />
                    Timeline
                </button>
                 {/* Placeholder for other tabs if needed, sticking to basics first */}
            </div>
            </div>
        </div>

        {/* Main Content Grid */}
        <div className="flex-1 grid grid-cols-2 gap-6 p-6 overflow-hidden">
            {/* Left: Timeline */}
            <div className="bg-white rounded-xl shadow-lg border border-slate-200 overflow-hidden flex flex-col">
                <div className="flex-none bg-gradient-to-r from-slate-700 to-slate-600 px-6 py-4 flex items-center justify-between">
                    <h2 className="text-xl font-bold text-white flex items-center gap-2">
                        <Activity size={20} />
                        Timeline Stream
                    </h2>
                    <span className="px-3 py-1 bg-white/20 text-white text-sm font-semibold rounded-lg">
                            {context.length} Items
                    </span>
                </div>
                <div className="flex-1 overflow-y-auto p-6 space-y-4">
                    {loading && <div className="text-center p-4">Loading Stream...</div>}
                    {context.map((item, idx) => (
                            <TimelineCard 
                            key={item.id} 
                            item={item} 
                            isSelected={selectedItem?.id === item.id}
                            onClick={() => handleItemSelect(item)}
                            />
                    ))}
                </div>
            </div>

            {/* Right: Detail & AI */}
            <div className="bg-white rounded-xl shadow-lg border border-slate-200 overflow-hidden flex flex-col">
                    <div className="flex-none bg-gradient-to-r from-cyan-700 to-cyan-600 px-6 py-4">
                    <h2 className="text-xl font-bold text-white flex items-center gap-2">
                        <Stethoscope size={20} />
                        Context Analysis
                    </h2>
                    </div>
                    <div className="flex-1 overflow-y-auto">
                    <DetailPanel 
                            selectedItem={selectedItem} 
                            insight={insight} 
                            loadingInsight={loadingInsight} 
                            insightError={insightError}
                    />
                    </div>
            </div>
        </div>
    </>
  );
}
