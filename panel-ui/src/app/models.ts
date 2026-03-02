// Shared model interfaces

export interface ApiError {
  error: {
    code: string;
    message: string;
    details: unknown;
  };
}

export interface User {
  id: string;
  username: string;
  email: string;
  firstName: string;
  lastName: string;
  role: 'admin' | 'user';
  hostingPlanId?: string;
}

export interface HostingPlan {
  id: string;
  name: string;
  displayName: string;
  limits: {
    websites: number;
    databases: number;
    emailAccounts: number;
    storageGB: number;
    cpuMillicores: number;
    memoryMB: number;
  };
}

export interface Website {
  id: string;
  name: string;
  primaryDomain: string;
  aliases: string[];
  php: { version: string };
  ssl?: { enabled: boolean; mode: string; issuer?: string; secretName?: string };
  status: { phase: string; replicas: number; readyReplicas: number; domains: DomainStatus[] };
  // legacy compat
  domains?: DomainConfig[];
}

export interface DomainConfig {
  name: string;
  ssl: { enabled: boolean; mode: string; issuer: string; secretName?: string };
}

export interface DomainStatus {
  name: string;
  certificateStatus: 'Pending' | 'Valid' | 'Expiring' | 'Expired' | 'Error';
  certificateExpiry?: string;
}

export interface Database {
  id: string;
  name: string;
  charset: string;
  collation: string;
  status: { phase: string; host: string; port: number; databaseName: string; username: string; password: string };
}

export interface EmailAccount {
  id: string;
  address: string;
  domain: string;
  quotaMB: number;
  status: { phase: string; usedMB: number };
}

export interface DnsZone {
  id: string;
  name: string;
  records: DnsRecord[];
  secondaryIps?: string[];
}

export interface DnsRecord {
  id?: string;
  name: string;
  type: 'A' | 'AAAA' | 'CNAME' | 'MX' | 'TXT' | 'SRV' | 'NS';
  content: string;
  ttl: number;
  priority?: number;
}

export interface Certificate {
  id: string;
  name: string;
  domain: string;
  status: 'Pending' | 'Valid' | 'Expiring' | 'Expired' | 'Error';
  expiry?: string;
  issuer: string;
  mode: 'letsencrypt' | 'selfsigned' | 'custom';
  namespace?: string;
  message?: string;
}

export interface Backup {
  id: string;
  createdAt: string;
  size: string;
  status: string;
  destination: string;
}

export interface BackupSchedule {
  id: string;
  cron: string;
  destination: string;
  retentionCount: number;
}

export interface HealthStatus {
  deployments: ComponentStatus[];
  statefulSets: ComponentStatus[];
  pods: PodStatus[];
}

export interface ComponentStatus {
  name: string;
  ready: number;
  desired: number;
  available: boolean;
}

export interface PodStatus {
  name: string;
  phase: string;
  restarts: number;
  ready: boolean;
  containers: { name: string; state: string; restartCount: number }[];
}
