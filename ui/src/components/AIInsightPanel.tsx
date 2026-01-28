import { type Bead } from '../lib/api';
import ReactMarkdown from 'react-markdown';
import { Sparkles, Loader2, AlertTriangle, ArrowLeft, Search } from 'lucide-react';

interface AIInsightPanelProps {
  selectedBead: Bead | null;
  insight: string | null;
  loading: boolean;
  error: string | null;
}

export default function AIInsightPanel({ selectedBead, insight, loading, error }: AIInsightPanelProps) {
  if (!selectedBead) {
    return (
      <div className="glass-panel p-8 flex flex-col items-center justify-center text-center opacity-60 min-h-[300px]">
        <ArrowLeft size={48} className="mb-4 text-[hsl(var(--color-primary-h),var(--color-primary-s),var(--color-primary-l))]" />
        <h3 className="text-xl font-bold mb-2">Detailed Analysis</h3>
        <p>Select an item from the timeline to view AI-generated context & clinical insights.</p>
      </div>
    );
  }

  // Pre-process text to convert [[Term]] to link [Term](term:Term)
  const processedInsight = insight?.replace(/\[\[(.*?)\]\]/g, '[$1](term:$1)') || '';

  return (
    <div className="glass-panel p-6 h-fit sticky top-8 animate-fadeIn">
      <div className="border-b border-[var(--glass-border)] pb-4 mb-4">
        <div className="flex items-center gap-2 text-[hsl(var(--color-primary-h),var(--color-primary-s),var(--color-primary-l))] mb-1">
          <Sparkles size={20} />
          <span className="font-bold uppercase tracking-wider text-sm">AI Clinical Insight</span>
        </div>
        <h2 className="text-xl font-bold">
           {selectedBead.type} Analysis
        </h2>
        <div className="text-xs opacity-50 font-mono mt-1">ID: {selectedBead.id.substring(0, 12)}...</div>
      </div>

      <div className="min-h-[200px]">
        {loading ? (
          <div className="flex flex-col items-center justify-center py-12 gap-4">
            <Loader2 className="animate-spin text-[hsl(var(--color-primary-h),var(--color-primary-s),var(--color-primary-l))]" size={32} />
            <p className="animate-pulse opacity-70">Analyzing patient history context...</p>
          </div>
        ) : error ? (
           <div className="p-4 rounded-lg bg-red-500/10 border border-red-500/30 text-red-200 flex items-start gap-3">
             <AlertTriangle className="shrink-0" />
             <div>
               <div className="font-bold">Analysis Failed</div>
               <div className="text-sm opacity-80 mt-1">{error}</div>
             </div>
           </div>
        ) : insight ? (
          <div className="prose prose-invert prose-sm max-w-none">
             <ReactMarkdown
                components={{
                    a: ({ node, ...props }) => {
                        const isTerm = props.href?.startsWith('term:');
                        if (isTerm) {
                            return (
                                <span 
                                    className="inline-flex items-center gap-1 text-[hsl(var(--color-secondary-h),var(--color-secondary-s),var(--color-secondary-l))] font-semibold bg-white/5 px-1 rounded cursor-pointer hover:bg-white/10 transition-colors"
                                    onClick={(e) => {
                                        e.preventDefault();
                                        alert(`Navigating to Context for: ${props.href?.replace('term:', '')} \n(Not implemented yet)`);
                                    }}
                                    title="View context for this term"
                                >
                                    <Search size={10} />
                                    {props.children}
                                </span>
                            );
                        }
                        return <a {...props} className="text-blue-400 underline" />;
                    }
                }}
             >
                {processedInsight}
             </ReactMarkdown>
             
            <div className="mt-6 pt-4 border-t border-[var(--glass-border)] text-xs opacity-50">
              Powered by MedBeads Context Engine & Gemini 3 Pro
            </div>
          </div>
        ) : (
          <p className="opacity-50">No insight generated.</p>
        )}
      </div>
    </div>
  );
}
