import { useState, useEffect } from 'react';
import { Search, Calendar, Building2, Stethoscope, Users, Clock, Filter, X } from 'lucide-react';
import { supabase, Patient } from '../lib/supabase';

interface PatientSearchProps {
  onSelectPatient: (patient: Patient) => void;
}

interface PatientWithEncounter extends Patient {
  lastEncounterDate?: string;
  lastDepartment?: string;
  encounterCount?: number;
  todayChiefComplaint?: string;
  encounterStatus?: 'waiting' | 'in_progress' | 'completed';
}

type ViewMode = 'today' | 'recent' | 'all';
type EncounterStatus = 'all' | 'waiting' | 'in_progress' | 'completed';

export function PatientSearch({ onSelectPatient }: PatientSearchProps) {
  const [patients, setPatients] = useState<PatientWithEncounter[]>([]);
  const [filteredPatients, setFilteredPatients] = useState<PatientWithEncounter[]>([]);
  const [searchTerm, setSearchTerm] = useState('');
  const [loading, setLoading] = useState(true);
  const [viewMode, setViewMode] = useState<ViewMode>('today');
  const [startDate, setStartDate] = useState('');
  const [endDate, setEndDate] = useState('');
  const [encounterStatus, setEncounterStatus] = useState<EncounterStatus>('all');
  const [selectedDepartment, setSelectedDepartment] = useState<string>('all');
  const [departments, setDepartments] = useState<string[]>([]);
  const [showFilters, setShowFilters] = useState(false);

  useEffect(() => {
    fetchPatientsWithEncounters();
    fetchDepartments();
  }, []);

  useEffect(() => {
    applyFilters();
  }, [searchTerm, viewMode, patients, startDate, endDate, encounterStatus, selectedDepartment]);

  async function fetchDepartments() {
    try {
      const { data } = await supabase
        .from('encounters')
        .select('department')
        .not('department', 'is', null);

      if (data) {
        const uniqueDepts = Array.from(new Set(data.map(d => d.department).filter(Boolean)));
        setDepartments(uniqueDepts as string[]);
      }
    } catch (error) {
      console.error('Error fetching departments:', error);
    }
  }

  const setTodayDate = () => {
    const today = new Date().toISOString().split('T')[0];
    setStartDate(today);
    setEndDate(today);
  };

  const clearFilters = () => {
    setStartDate('');
    setEndDate('');
    setEncounterStatus('all');
    setSelectedDepartment('all');
  };

  async function fetchPatientsWithEncounters() {
    try {
      setLoading(true);
      const { data: patientsData, error: patientsError } = await supabase
        .from('patients')
        .select('*')
        .order('created_at', { ascending: false });

      if (patientsError) throw patientsError;

      const patientsWithEncounters = await Promise.all(
        (patientsData || []).map(async (patient) => {
          const { data: encounters } = await supabase
            .from('encounters')
            .select('encounter_date, department, chief_complaint, status')
            .eq('patient_id', patient.id)
            .order('encounter_date', { ascending: false })
            .limit(1);

          const { data: todayEncounter } = await supabase
            .from('encounters')
            .select('chief_complaint, status')
            .eq('patient_id', patient.id)
            .gte('encounter_date', new Date().toISOString().split('T')[0])
            .limit(1);

          const { count } = await supabase
            .from('encounters')
            .select('*', { count: 'exact', head: true })
            .eq('patient_id', patient.id);

          return {
            ...patient,
            lastEncounterDate: encounters?.[0]?.encounter_date,
            lastDepartment: encounters?.[0]?.department,
            encounterCount: count || 0,
            todayChiefComplaint: todayEncounter?.[0]?.chief_complaint,
            encounterStatus: todayEncounter?.[0]?.status || encounters?.[0]?.status,
          };
        })
      );

      setPatients(patientsWithEncounters);
    } catch (error) {
      console.error('Error fetching patients:', error);
    } finally {
      setLoading(false);
    }
  }

  function applyFilters() {
    let filtered = [...patients];

    if (viewMode === 'today') {
      filtered = filtered.filter((p) => {
        if (!p.lastEncounterDate) return false;
        const encounterDate = new Date(p.lastEncounterDate);
        const today = new Date();
        return encounterDate.toDateString() === today.toDateString();
      });
    } else if (viewMode === 'recent') {
      filtered = filtered.filter((p) => {
        if (!p.lastEncounterDate) return false;
        const encounterDate = new Date(p.lastEncounterDate);
        const weekAgo = new Date();
        weekAgo.setDate(weekAgo.getDate() - 7);
        return encounterDate >= weekAgo;
      });
    }

    if (startDate || endDate) {
      filtered = filtered.filter((p) => {
        if (!p.lastEncounterDate) return false;
        const encounterDate = new Date(p.lastEncounterDate);
        if (startDate && encounterDate < new Date(startDate)) return false;
        if (endDate) {
          const end = new Date(endDate);
          end.setHours(23, 59, 59, 999);
          if (encounterDate > end) return false;
        }
        return true;
      });
    }

    if (encounterStatus !== 'all') {
      filtered = filtered.filter((p) => p.encounterStatus === encounterStatus);
    }

    if (selectedDepartment !== 'all') {
      filtered = filtered.filter((p) => p.lastDepartment === selectedDepartment);
    }

    if (searchTerm.trim() !== '') {
      filtered = filtered.filter(
        (p) =>
          p.patient_identifier.toLowerCase().includes(searchTerm.toLowerCase()) ||
          p.family_name.includes(searchTerm) ||
          p.given_name.includes(searchTerm)
      );
    }

    setFilteredPatients(filtered);
  }

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
      <div className="bg-white border-b border-slate-200 shadow-sm">
        <div className="max-w-7xl mx-auto px-6 py-4">
          <h1 className="text-2xl font-bold text-slate-900">電子カルテシステム</h1>
        </div>
      </div>

      <div className="max-w-7xl mx-auto px-6 py-6">
        <div className="bg-white rounded-lg shadow-sm border border-slate-200 overflow-hidden">
          <div className="border-b border-slate-200">
            <div className="flex items-center gap-1 px-6 pt-4">
              <button
                onClick={() => setViewMode('today')}
                className={`px-4 py-2 font-medium text-sm border-b-2 transition-colors ${
                  viewMode === 'today'
                    ? 'border-blue-600 text-blue-600'
                    : 'border-transparent text-slate-600 hover:text-slate-900 hover:border-slate-300'
                }`}
              >
                本日受診
              </button>
              <button
                onClick={() => setViewMode('recent')}
                className={`px-4 py-2 font-medium text-sm border-b-2 transition-colors ${
                  viewMode === 'recent'
                    ? 'border-blue-600 text-blue-600'
                    : 'border-transparent text-slate-600 hover:text-slate-900 hover:border-slate-300'
                }`}
              >
                最近の受診
              </button>
              <button
                onClick={() => setViewMode('all')}
                className={`px-4 py-2 font-medium text-sm border-b-2 transition-colors ${
                  viewMode === 'all'
                    ? 'border-blue-600 text-blue-600'
                    : 'border-transparent text-slate-600 hover:text-slate-900 hover:border-slate-300'
                }`}
              >
                全患者
              </button>
            </div>
          </div>

          <div className="p-6 border-b border-slate-200 bg-slate-50">
            <div className="mb-4">
              <button
                onClick={() => setShowFilters(!showFilters)}
                className="flex items-center gap-2 px-4 py-2 text-sm font-medium text-slate-700 bg-white border border-slate-300 rounded-lg hover:bg-slate-50 transition-colors"
              >
                <Filter className="w-4 h-4" />
                絞り込み条件
                {(startDate || endDate || encounterStatus !== 'all' || selectedDepartment !== 'all') && (
                  <span className="ml-1 px-2 py-0.5 bg-blue-100 text-blue-700 text-xs font-semibold rounded-full">
                    適用中
                  </span>
                )}
              </button>
            </div>

            {showFilters && (
              <div className="mb-4 p-4 bg-white border border-slate-300 rounded-lg space-y-4">
                <div className="grid grid-cols-1 md:grid-cols-3 gap-4">
                  <div className="space-y-2">
                    <label className="block text-sm font-medium text-slate-700">受診期間</label>
                    <div className="space-y-2">
                      <div className="flex items-center gap-2">
                        <input
                          type="date"
                          value={startDate}
                          onChange={(e) => setStartDate(e.target.value)}
                          className="flex-1 px-3 py-2 text-sm border border-slate-300 rounded-lg focus:outline-none focus:border-blue-500 focus:ring-1 focus:ring-blue-500"
                        />
                        <span className="text-slate-500">〜</span>
                        <input
                          type="date"
                          value={endDate}
                          onChange={(e) => setEndDate(e.target.value)}
                          className="flex-1 px-3 py-2 text-sm border border-slate-300 rounded-lg focus:outline-none focus:border-blue-500 focus:ring-1 focus:ring-blue-500"
                        />
                      </div>
                      <button
                        onClick={setTodayDate}
                        className="text-xs text-blue-600 hover:text-blue-700 font-medium"
                      >
                        本日を設定
                      </button>
                    </div>
                  </div>

                  <div className="space-y-2">
                    <label className="block text-sm font-medium text-slate-700">診療科</label>
                    <select
                      value={selectedDepartment}
                      onChange={(e) => setSelectedDepartment(e.target.value)}
                      className="w-full px-3 py-2 text-sm border border-slate-300 rounded-lg focus:outline-none focus:border-blue-500 focus:ring-1 focus:ring-blue-500"
                    >
                      <option value="all">すべて</option>
                      {departments.map((dept) => (
                        <option key={dept} value={dept}>
                          {dept}
                        </option>
                      ))}
                    </select>
                  </div>

                  <div className="space-y-2">
                    <label className="block text-sm font-medium text-slate-700">受診状態</label>
                    <div className="space-y-2">
                      <label className="flex items-center gap-2 cursor-pointer">
                        <input
                          type="radio"
                          name="status"
                          checked={encounterStatus === 'all'}
                          onChange={() => setEncounterStatus('all')}
                          className="w-4 h-4 text-blue-600 border-slate-300 focus:ring-blue-500"
                        />
                        <span className="text-sm text-slate-700">すべて</span>
                      </label>
                      <label className="flex items-center gap-2 cursor-pointer">
                        <input
                          type="radio"
                          name="status"
                          checked={encounterStatus === 'waiting'}
                          onChange={() => setEncounterStatus('waiting')}
                          className="w-4 h-4 text-blue-600 border-slate-300 focus:ring-blue-500"
                        />
                        <span className="text-sm text-slate-700">受付中</span>
                      </label>
                      <label className="flex items-center gap-2 cursor-pointer">
                        <input
                          type="radio"
                          name="status"
                          checked={encounterStatus === 'in_progress'}
                          onChange={() => setEncounterStatus('in_progress')}
                          className="w-4 h-4 text-blue-600 border-slate-300 focus:ring-blue-500"
                        />
                        <span className="text-sm text-slate-700">受診中</span>
                      </label>
                      <label className="flex items-center gap-2 cursor-pointer">
                        <input
                          type="radio"
                          name="status"
                          checked={encounterStatus === 'completed'}
                          onChange={() => setEncounterStatus('completed')}
                          className="w-4 h-4 text-blue-600 border-slate-300 focus:ring-blue-500"
                        />
                        <span className="text-sm text-slate-700">診察終了</span>
                      </label>
                    </div>
                  </div>
                </div>

                <div className="flex items-center gap-2 pt-2 border-t border-slate-200">
                  <button
                    onClick={clearFilters}
                    className="flex items-center gap-1 px-3 py-1.5 text-sm font-medium text-slate-600 hover:text-slate-900 transition-colors"
                  >
                    <X className="w-4 h-4" />
                    クリア
                  </button>
                </div>
              </div>
            )}

            <div className="relative">
              <Search className="absolute left-4 top-1/2 transform -translate-y-1/2 text-slate-400 w-5 h-5" />
              <input
                type="text"
                placeholder="患者ID、氏名で検索..."
                value={searchTerm}
                onChange={(e) => setSearchTerm(e.target.value)}
                className="w-full pl-12 pr-4 py-3 rounded-lg border border-slate-300 bg-white text-slate-900 placeholder-slate-400 focus:outline-none focus:border-blue-500 focus:ring-2 focus:ring-blue-500/20 transition-all"
              />
            </div>
          </div>

          <div className="divide-y divide-slate-200">
            {loading ? (
              <div className="flex items-center justify-center py-16">
                <div className="animate-spin rounded-full h-12 w-12 border-4 border-blue-600 border-t-transparent"></div>
              </div>
            ) : filteredPatients.length === 0 ? (
              <div className="text-center py-16 px-6 text-slate-500">
                <p className="text-base">該当する患者が見つかりませんでした</p>
              </div>
            ) : (
              <>
                <div className="px-6 py-3 bg-slate-50">
                  <p className="text-sm font-medium text-slate-600">
                    {filteredPatients.length}件の患者
                  </p>
                </div>
                {filteredPatients.map((patient) => {
                  const isToday = patient.lastEncounterDate &&
                    new Date(patient.lastEncounterDate).toDateString() === new Date().toDateString();

                  return (
                    <button
                      key={patient.id}
                      onClick={() => onSelectPatient(patient)}
                      className="w-full text-left px-6 py-4 hover:bg-blue-50 transition-colors group"
                    >
                      <div className="flex items-start justify-between">
                        <div className="flex-1 min-w-0">
                          <div className="flex items-center gap-3 mb-2">
                            <span className="px-2.5 py-0.5 bg-slate-100 text-slate-700 text-xs font-mono font-semibold rounded">
                              {patient.patient_identifier}
                            </span>
                            <h3 className="text-base font-bold text-slate-900">
                              {patient.family_name} {patient.given_name}
                            </h3>
                            <span className="text-sm text-slate-600">
                              {calculateAge(patient.birth_date)}歳 ・ {patient.gender === 'male' ? '男性' : '女性'}
                            </span>
                            {isToday && patient.encounterStatus === 'waiting' && (
                              <span className="px-2 py-0.5 bg-yellow-100 text-yellow-700 text-xs font-semibold rounded">
                                受付中
                              </span>
                            )}
                            {isToday && patient.encounterStatus === 'in_progress' && (
                              <span className="px-2 py-0.5 bg-green-100 text-green-700 text-xs font-semibold rounded">
                                受診中
                              </span>
                            )}
                            {isToday && patient.encounterStatus === 'completed' && (
                              <span className="px-2 py-0.5 bg-slate-100 text-slate-700 text-xs font-semibold rounded">
                                診察終了
                              </span>
                            )}
                          </div>
                          <div className="flex items-center gap-4 text-sm text-slate-600">
                            {patient.lastDepartment && (
                              <div className="flex items-center gap-1.5">
                                <Stethoscope className="w-4 h-4" />
                                <span>{patient.lastDepartment}</span>
                              </div>
                            )}
                            {patient.lastEncounterDate && (
                              <div className="flex items-center gap-1.5">
                                <Calendar className="w-4 h-4" />
                                <span>
                                  {new Date(patient.lastEncounterDate).toLocaleDateString('ja-JP')} {new Date(patient.lastEncounterDate).toLocaleTimeString('ja-JP', { hour: '2-digit', minute: '2-digit' })}
                                </span>
                              </div>
                            )}
                          </div>
                          {patient.todayChiefComplaint && (
                            <div className="mt-2 text-sm text-slate-700">
                              <span className="font-medium">主訴：</span>
                              {patient.todayChiefComplaint}
                            </div>
                          )}
                        </div>
                        <div className="flex items-center text-blue-600 opacity-0 group-hover:opacity-100 transition-opacity ml-4">
                          <svg
                            className="w-5 h-5"
                            fill="none"
                            stroke="currentColor"
                            viewBox="0 0 24 24"
                          >
                            <path
                              strokeLinecap="round"
                              strokeLinejoin="round"
                              strokeWidth={2}
                              d="M9 5l7 7-7 7"
                            />
                          </svg>
                        </div>
                      </div>
                    </button>
                  );
                })}
              </>
            )}
          </div>
        </div>
      </div>
    </div>
  );
}
