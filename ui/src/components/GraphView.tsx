import { useMemo, useCallback } from 'react';
import ReactFlow, {
  Background,
  Controls,
  MiniMap,
  MarkerType,
  Position,
  ReactFlowProvider,
  type Node,
  type Edge
} from 'reactflow';
import 'reactflow/dist/style.css';
import { type TimelineItem } from '../lib/api';

interface GraphViewProps {
  items: TimelineItem[];
  onNodeClick?: (item: TimelineItem) => void;
  selectedId?: string;
}

const nodeWidth = 180;
const nodeHeight = 70;
const xGap = 30;
const yGap = 120;

// Define nodeTypes and edgeTypes outside component to avoid React Flow warning
const nodeTypes = {};
const edgeTypes = {};

// Custom layout: Group by Date (Y axis = Time, X axis = Items in same date)
const getLayoutedElements = (nodes: Node[], edges: Edge[], items: TimelineItem[]) => {
  // 1. Group nodes by date
  const nodesByDate: Record<string, Node[]> = {};
  
  nodes.forEach(node => {
    const item = items.find(i => i.data.id === node.id);
    if (item && item.date) {
        const dateKey = item.date.length >= 10 ? item.date.substring(0, 10) : 'unknown';
        if (!nodesByDate[dateKey]) {
            nodesByDate[dateKey] = [];
        }
        nodesByDate[dateKey].push(node);
    } else {
        if (!nodesByDate['unknown']) nodesByDate['unknown'] = [];
        nodesByDate['unknown'].push(node);
    }
  });

  // 2. Sort dates (Newest first -> Top)
  const sortedDates = Object.keys(nodesByDate).sort((a, b) => {
      if (a === 'unknown') return 1;
      if (b === 'unknown') return -1;
      return b.localeCompare(a);
  });

  // 3. Assign positions - Start from 0,0, no negative coordinates
  let currentY = 0;

  sortedDates.forEach(date => {
    const rowNodes = nodesByDate[date];
    
    // Sort nodes within row by type
    rowNodes.sort((a, b) => { 
        const typeA = items.find(i => i.data.id === a.id)?.type || '';
        const typeB = items.find(i => i.data.id === b.id)?.type || '';
        return typeA.localeCompare(typeB);
    });

    // Position nodes starting from x=0
    rowNodes.forEach((node, index) => {
      node.position = {
        x: index * (nodeWidth + xGap),
        y: currentY
      };
      
      node.sourcePosition = Position.Bottom; 
      node.targetPosition = Position.Top;
    });

    currentY += nodeHeight + yGap;
  });

  return { nodes, edges };
};

function GraphViewInner({ items, onNodeClick, selectedId }: GraphViewProps) {
  const { nodes: initialNodes, edges: initialEdges } = useMemo(() => {
    const newNodes: Node[] = [];
    const newEdges: Edge[] = [];
    const idSet = new Set(items.map(item => item.data.id));

    items.forEach((item) => {
      const itemId = item.data.id;
      const isSelected = selectedId === itemId;
      
      let label = item.type.toUpperCase();
      let bgColor = "rgba(255, 255, 255, 0.95)";
      let borderColor = "#cbd5e1";

      if (item.type === "medication") {
        label = "💊 Medication";
        bgColor = isSelected ? '#dbeafe' : '#f0fdf4';
        borderColor = isSelected ? '#2563eb' : '#16a34a';
      } else if (item.type === "observation") {
        label = "📈 Observation";
        bgColor = isSelected ? '#dbeafe' : '#f8fafc';
        borderColor = isSelected ? '#2563eb' : '#64748b';
      } else if (item.type === "encounter") {
        label = "🩺 Encounter";
        bgColor = isSelected ? '#dbeafe' : '#fff7ed';
        borderColor = isSelected ? '#2563eb' : '#f97316';
      } else if (item.type === "condition") {
        label = "⚠️ Condition";
        bgColor = isSelected ? '#dbeafe' : '#fef2f2';
        borderColor = isSelected ? '#2563eb' : '#dc2626';
      } else if (item.type === "diagnostic_report") {
        label = "📄 Report";
        bgColor = isSelected ? '#dbeafe' : '#f0f9ff';
        borderColor = isSelected ? '#2563eb' : '#0891b2';
      }

      let detail = "";
      if (item.type === "medication") detail = item.data.medication_name;
      if (item.type === "observation") detail = item.data.display_name;
      if (item.type === "encounter") detail = item.data.encounter_type;
      if (item.type === "condition") detail = item.data.condition_name;
      if (item.type === "diagnostic_report") detail = item.data.title;

      if (detail && detail.length > 20) detail = detail.substring(0, 18) + "...";

      newNodes.push({
        id: itemId,
        data: { label: `${label}\n${detail}\n${new Date(item.date).toLocaleDateString()}` },
        position: { x: 0, y: 0 }, 
        style: { 
            background: bgColor, 
            border: `2px solid ${borderColor}`,
            color: '#1e293b',
            borderRadius: '10px',
            fontSize: '11px',
            padding: '8px',
            width: nodeWidth,
            textAlign: 'center' as const,
            whiteSpace: 'pre-wrap' as const,
            fontWeight: '500',
            boxShadow: isSelected ? '0 0 0 3px rgba(37, 99, 235, 0.3)' : '0 1px 3px rgba(0,0,0,0.1)',
            cursor: 'pointer'
        },
      });

      // Edges (Parent -> Child)
      item.parents.forEach((parentId) => {
        if (idSet.has(parentId)) {
            newEdges.push({
                id: `e-${parentId}-${itemId}`,
                source: parentId,
                target: itemId,
                type: 'smoothstep',
                animated: false,
                style: { stroke: '#94a3b8', strokeWidth: 1 },
                markerEnd: {
                    type: MarkerType.ArrowClosed,
                    color: '#94a3b8',
                    width: 15,
                    height: 15,
                },
            });
        }
      });
    });

    return getLayoutedElements(newNodes, newEdges, items);
  }, [items, selectedId]);

  const handleNodeClick = useCallback((_: React.MouseEvent, node: Node) => {
    const item = items.find(i => i.data.id === node.id);
    if (item && onNodeClick) onNodeClick(item);
  }, [items, onNodeClick]);

  return (
    <ReactFlow
      nodes={initialNodes}
      edges={initialEdges}
      nodeTypes={nodeTypes}
      edgeTypes={edgeTypes}
      onNodeClick={handleNodeClick}
      fitView
      fitViewOptions={{ padding: 0.2, maxZoom: 1 }}
      minZoom={0.05}
      maxZoom={2}
      attributionPosition="bottom-right"
      proOptions={{ hideAttribution: true }}
    >
      <Background color="#e2e8f0" gap={20} size={1} />
      <Controls showInteractive={false} className="bg-white border border-slate-200 shadow-sm" />
      <MiniMap 
          nodeColor={(n) => {
              if (n.style?.background) return n.style.background as string;
              return '#fff';
          }}
          nodeBorderRadius={2}
          className="border border-slate-200 shadow-lg rounded-lg overflow-hidden" 
      />
    </ReactFlow>
  );
}

export default function GraphView(props: GraphViewProps) {
  return (
    <div style={{ width: '100%', height: '100%' }} className="bg-slate-50">
      <ReactFlowProvider>
        <GraphViewInner {...props} />
      </ReactFlowProvider>
    </div>
  );
}
