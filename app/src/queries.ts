import { useQuery } from '@tanstack/react-query';
import { useSchall } from '@/auth/session';

/** The two requests the review screen makes, and nothing else: every field a
 * row needs is in these payloads (docs/api.md, "What a review row carries"). */
export function useReviewQueue() {
  const { api } = useSchall();
  return useQuery({ queryKey: ['review-queue'], queryFn: () => api.reviewQueue() });
}

export function useReviewFolders() {
  const { api } = useSchall();
  return useQuery({ queryKey: ['downloads', 'review'], queryFn: () => api.downloads('review') });
}
