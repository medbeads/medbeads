import { useState, useEffect } from 'react';
import { ArrowLeft, Filter, Activity, Pill, FileText, AlertCircle, Plus, Stethoscope, Heart, Thermometer } from 'lucide-react';
import {
  supabase,
  Patient,
  TimelineItem,
  Encounter,
  Medication,
  Observation,
  DiagnosticReport,
  Condition,
} from '../lib/supabase';
import { TimelineCard } from './TimelineCard';
import { DetailPanel } from './DetailPanel';

interface EMRViewProps {
  patient: Patient;
  onBack: () => void;
}

type TabType = 'timeline' | 'medications' | 'observations' | 'reports' | 'conditions';

export function EMRView({ patient, onBack }: EMRViewProps) {
  const [timelineItems, setTimelineItems] = useState<TimelineItem[]>([]);
  const [selectedItem, setSelectedItem] = useState<TimelineItem | null>(null);
  const [loading, setLoading] = useState(true);
  const [activeTab, setActiveTab] = useState<TabType>('timeline');
  const [latestVitals, setLatestVitals] = useState<Observation[]>([]);
  const [activeConditions, setActiveConditions] = useState<Condition[]>([]);
  const [currentMedications, setCurrentMedications] = useState<Medication[]>([]);

  useEffect(() => {
    fetchTimelineData();
  }, [patient.id]);

  async function fetchTimelineData() {
    try {
      setLoading(true);
      const [encounters, medications, observations, reports, conditions] = await Promise.all([
        supabase
          .from('encounters')
          .select('*')
          .eq('patient_id', patient.id)
          .order('encounter_date', { ascending: false }),
        supabase
          .from('medications')
          .select('*')
          .eq('patient_id', patient.id)
          .order('prescribed_date', { ascending: false }),
        supabase
          .from('observations')
          .select('*')
          .eq('patient_id', patient.id)
          .order('observation_date', { ascending: false }),
        supabase
          .from('diagnostic_reports')
          .select('*')
          .eq('patient_id', patient.id)
          .order('report_date', { ascending: false }),
        supabase
          .from('conditions')
          .select('*')
          .eq('patient_id', patient.id)
          .order('recorded_date', { ascending: false }),
      ]);

      const items: TimelineItem[] = [
        ...(encounters.data?.map((e) => ({
          type: 'encounter' as const,
          date: e.encounter_date,
          data: e,
        })) || []),
        ...(medications.data?.map((m) => ({
          type: 'medication' as const,
          date: m.prescribed_date,
          data: m,
        })) || []),
        ...(observations.data?.map((o) => ({
          type: 'observation' as const,
          date: o.observation_date,
          data: o,
        })) || []),
        ...(reports.data?.map((r) => ({
          type: 'diagnostic_report' as const,
          date: r.report_date,
          data: r,
        })) || []),
        ...(conditions.data?.map((c) => ({
          type: 'condition' as const,
          date: c.recorded_date,
          data: c,
        })) || []),
      ];

      items.sort((a, b) => new Date(b.date).getTime() - new Date(a.date).getTime());
      setTimelineItems(items);

      const vitals = observations.data?.filter(o => o.observation_type === 'vital-signs').slice(0, 4) || [];
      setLatestVitals(vitals);

      const active = conditions.data?.filter(c => c.clinical_status === 'active').slice(0, 3) || [];
      setActiveConditions(active);

      const recent = medications.data?.slice(0, 3) || [];
      setCurrentMedications(recent);
    } catch (error) {
      console.error('Error fetching timeline data:', error);
    } finally {
      setLoading(false);
    }
  }

  const getFilteredItems = () => {
    switch (activeTab) {
      case 'medications':
        return timelineItems.filter(item => item.type === 'medication');
      case 'observations':
        return timelineItems.filter(item => item.type === 'observation');
      case 'reports':
        return timelineItems.filter(item => item.type === 'diagnostic_report');
      case 'conditions':
        return timelineItems.filter(item => item.type === 'condition');
      default:
        return timelineItems;
    }
  };

  const filteredItems = getFilteredItems();

  const calculateAge = (birthDate: string) => {
    const today = new Date();
    const birth = new Date(birthDate);
    let age = today.getFullYear() - birth.getFullYear();
    const monthDiff = today.getMonth() - birth.getMonth();
    if (monthDiff < 0 || (monthDiff === 0 && today.getDate() < birth.getDate())) {
      age--;
    }
    return age;
  };

  return (
    <div className="min-h-screen bg-slate-50">
      <div className="bg-gradient-to-r from-blue-600 to-blue-700 text-white px-6 py-6 shadow-lg">
        <div className="max-w-[1920px] mx-auto">
          <button
            onClick={onBack}
            className="flex items-center gap-2 text-white/90 hover:text-white mb-4 transition-colors"
          >
            <ArrowLeft className="w-5 h-5" />
            <span className="text-sm font-medium">患者一覧に戻る</span>
          </button>
          <div className="flex items-start justify-between">
            <div>
              <div className="flex items-center gap-4 mb-2">
                <span className="px-3 py-1 bg-white/20 backdrop-blur-sm text-white text-sm font-mono font-semibold rounded-lg">
                  {patient.patient_identifier}
                </span>
                <h1 className="text-3xl font-bold">
                  {patient.family_name} {patient.given_name}
                </h1>
              </div>
              <p className="text-white/90 text-lg">
                {new Date(patient.birth_date).toLocaleDateString('ja-JP')} (
                {calculateAge(patient.birth_date)}歳) ・{' '}
                {patient.gender === 'male' ? '男性' : '女性'}
              </p>
            </div>
          </div>
        </div>
      </div>

      <div className="bg-white border-b border-slate-200 shadow-sm">
        <div className="max-w-[1920px] mx-auto px-6 py-4">
          <div className="grid grid-cols-1 md:grid-cols-3 gap-4">
            <div className="bg-gradient-to-br from-red-50 to-pink-50 rounded-lg p-4 border border-red-200">
              <div className="flex items-center gap-2 mb-3">
                <Heart className="w-5 h-5 text-red-600" />
                <h3 className="font-bold text-slate-900">バイタルサイン</h3>
              </div>
              {latestVitals.length > 0 ? (
                <div className="grid grid-cols-2 gap-2">
                  {latestVitals.map((vital) => (
                    <div key={vital.id} className="text-sm">
                      <div className="text-slate-600">{vital.display_name}</div>
                      <div className="font-bold text-slate-900">
                        {vital.value_quantity} {vital.value_unit}
                      </div>
                    </div>
                  ))}
                </div>
              ) : (
                <p className="text-sm text-slate-500">データなし</p>
              )}
            </div>

            <div className="bg-gradient-to-br from-amber-50 to-orange-50 rounded-lg p-4 border border-amber-200">
              <div className="flex items-center gap-2 mb-3">
                <AlertCircle className="w-5 h-5 text-amber-600" />
                <h3 className="font-bold text-slate-900">現在の診断</h3>
              </div>
              {activeConditions.length > 0 ? (
                <div className="space-y-1">
                  {activeConditions.map((condition) => (
                    <div key={condition.id} className="text-sm">
                      <div className="font-semibold text-slate-900">{condition.condition_name}</div>
                      <div className="text-xs text-slate-600">重症度: {condition.severity}</div>
                    </div>
                  ))}
                </div>
              ) : (
                <p className="text-sm text-slate-500">データなし</p>
              )}
            </div>

            <div className="bg-gradient-to-br from-green-50 to-emerald-50 rounded-lg p-4 border border-green-200">
              <div className="flex items-center gap-2 mb-3">
                <Pill className="w-5 h-5 text-green-600" />
                <h3 className="font-bold text-slate-900">現在の処方</h3>
              </div>
              {currentMedications.length > 0 ? (
                <div className="space-y-1">
                  {currentMedications.map((med) => (
                    <div key={med.id} className="text-sm">
                      <div className="font-semibold text-slate-900">{med.medication_name}</div>
                      <div className="text-xs text-slate-600">{med.dosage} - {med.frequency}</div>
                    </div>
                  ))}
                </div>
              ) : (
                <p className="text-sm text-slate-500">データなし</p>
              )}
            </div>
          </div>
        </div>
      </div>

      <div className="bg-white border-b border-slate-200">
        <div className="max-w-[1920px] mx-auto px-6">
          <div className="flex items-center gap-1">
            {[
              { key: 'timeline', label: '全体タイムライン', icon: Activity },
              { key: 'medications', label: '処方薬', icon: Pill },
              { key: 'observations', label: '検査結果', icon: Stethoscope },
              { key: 'reports', label: '診断レポート', icon: FileText },
              { key: 'conditions', label: '診断・病態', icon: AlertCircle },
            ].map(({ key, label, icon: Icon }) => (
              <button
                key={key}
                onClick={() => setActiveTab(key as TabType)}
                className={`flex items-center gap-2 px-4 py-3 font-medium text-sm border-b-2 transition-colors ${
                  activeTab === key
                    ? 'border-blue-600 text-blue-600 bg-blue-50'
                    : 'border-transparent text-slate-600 hover:text-slate-900 hover:bg-slate-50'
                }`}
              >
                <Icon className="w-4 h-4" />
                {label}
              </button>
            ))}
          </div>
        </div>
      </div>

      <div className="max-w-[1920px] mx-auto px-6 py-6">
        <div className="grid grid-cols-1 lg:grid-cols-2 gap-6 h-[calc(100vh-440px)]">
          <div className="bg-white rounded-xl shadow-lg border border-slate-200 overflow-hidden flex flex-col">
            <div className="bg-gradient-to-r from-slate-700 to-slate-600 px-6 py-4 flex items-center justify-between">
              <h2 className="text-xl font-bold text-white">
                {activeTab === 'timeline' && '全体タイムライン'}
                {activeTab === 'medications' && '処方薬一覧'}
                {activeTab === 'observations' && '検査結果一覧'}
                {activeTab === 'reports' && '診断レポート一覧'}
                {activeTab === 'conditions' && '診断・病態一覧'}
              </h2>
              <span className="px-3 py-1 bg-white/20 text-white text-sm font-semibold rounded-lg">
                {filteredItems.length}件
              </span>
            </div>
            <div className="flex-1 overflow-y-auto p-6">
              {loading ? (
                <div className="flex items-center justify-center py-16">
                  <div className="animate-spin rounded-full h-12 w-12 border-4 border-blue-600 border-t-transparent"></div>
                </div>
              ) : filteredItems.length === 0 ? (
                <div className="text-center py-16 text-slate-500">
                  <Activity className="w-16 h-16 mx-auto mb-4 text-slate-300" />
                  <p className="text-lg font-medium">データがありません</p>
                  <p className="text-sm mt-2">この患者のデータはまだ登録されていません</p>
                </div>
              ) : (
                <div className="space-y-4">
                  {filteredItems.map((item, index) => (
                    <TimelineCard
                      key={`${item.type}-${(item.data as any).id}-${index}`}
                      item={item}
                      isSelected={selectedItem === item}
                      onClick={() => setSelectedItem(item)}
                    />
                  ))}
                </div>
              )}
            </div>
          </div>

          <div className="bg-white rounded-xl shadow-lg border border-slate-200 overflow-hidden flex flex-col">
            <div className="bg-gradient-to-r from-cyan-700 to-cyan-600 px-6 py-4">
              <h2 className="text-xl font-bold text-white">詳細情報・AIインサイト</h2>
            </div>
            <div className="flex-1 overflow-y-auto">
              {selectedItem ? (
                <DetailPanel selectedItem={selectedItem} patient={patient} />
              ) : (
                <div className="flex items-center justify-center h-full p-6 text-slate-500">
                  <div className="text-center">
                    <FileText className="w-16 h-16 mx-auto mb-4 text-slate-300" />
                    <p className="text-lg font-medium">項目を選択してください</p>
                    <p className="text-sm mt-2">左側のリストから項目を選択すると詳細が表示されます</p>
                  </div>
                </div>
              )}
            </div>
          </div>
        </div>
      </div>
    </div>
  );
}
